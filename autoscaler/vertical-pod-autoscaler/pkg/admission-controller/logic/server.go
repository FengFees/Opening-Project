/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package logic

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"k8s.io/api/admission/v1beta1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/limitrange"
	metrics_admission "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/admission"
	vpa_api_util "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
	"k8s.io/klog"
)

// AdmissionServer is an admission webhook server that modifies pod resources request based on VPA recommendation
type AdmissionServer struct {
	recommendationProvider RecommendationProvider
	podPreProcessor        PodPreProcessor
	vpaPreProcessor        VpaPreProcessor
	limitsChecker          limitrange.LimitRangeCalculator
}

// NewAdmissionServer constructs new AdmissionServer
func NewAdmissionServer(recommendationProvider RecommendationProvider, podPreProcessor PodPreProcessor, vpaPreProcessor VpaPreProcessor, limitsChecker limitrange.LimitRangeCalculator) *AdmissionServer {
	return &AdmissionServer{recommendationProvider, podPreProcessor, vpaPreProcessor, limitsChecker}
}

type patchRecord struct {
	Op    string      `json:"op,inline"`
	Path  string      `json:"path,inline"`
	Value interface{} `json:"value"`
}

func (s *AdmissionServer) getPatchesForPodResourceRequest(raw []byte, namespace string) ([]patchRecord, error) {
	pod := v1.Pod{}
	if err := json.Unmarshal(raw, &pod); err != nil {
		return nil, err
	}
	if len(pod.Name) == 0 {
		pod.Name = pod.GenerateName + "%"
		pod.Namespace = namespace
	}
	klog.V(4).Infof("Admitting pod %v", pod.ObjectMeta)
	containersResources, annotationsPerContainer, vpaName, err := s.recommendationProvider.GetContainersResourcesForPod(&pod)
	// 获取pod中的容器资源
	if err != nil {
		return nil, err
	}
	pod, err = s.podPreProcessor.Process(pod)
	// 预处理
	if err != nil {
		return nil, err
	}
	if annotationsPerContainer == nil {
		annotationsPerContainer = vpa_api_util.ContainerToAnnotationsMap{}
	}

	patches := []patchRecord{}
	updatesAnnotation := []string{}
	for i, containerResources := range containersResources {
		newPatches, newUpdatesAnnotation := s.getContainerPatch(pod, i, annotationsPerContainer, containerResources)
		// 对容器中一些信息进行填补
		patches = append(patches, newPatches...)
		updatesAnnotation = append(updatesAnnotation, newUpdatesAnnotation)
		// 获取填补的patches和更新的注释updatesAnnotation
	}
	if len(updatesAnnotation) > 0 {
		// 如果更新了注释，说明pod资源进行了更新
		vpaAnnotationValue := fmt.Sprintf("Pod resources updated by %s: %s", vpaName, strings.Join(updatesAnnotation, "; "))
		if pod.Annotations == nil {
			patches = append(patches, patchRecord{
				Op:    "add",
				Path:  "/metadata/annotations",
				Value: map[string]string{"vpaUpdates": vpaAnnotationValue}})
		} else {
			patches = append(patches, patchRecord{
				Op:    "add",
				Path:  "/metadata/annotations/vpaUpdates",
				Value: vpaAnnotationValue})
		}
		// 对pod的注释变量进行赋值
	}
	return patches, nil
}

func getPatchInitializingEmptyResources(i int) patchRecord {
	return patchRecord{
		Op:    "add",
		Path:  fmt.Sprintf("/spec/containers/%d/resources", i),
		Value: v1.ResourceRequirements{},
	}
}

func getPatchInitializingEmptyResourcesSubfield(i int, kind string) patchRecord {
	return patchRecord{
		Op:    "add",
		Path:  fmt.Sprintf("/spec/containers/%d/resources/%s", i, kind),
		Value: v1.ResourceList{},
	}
}

func getAddResourceRequirementValuePatch(i int, kind string, resource v1.ResourceName, quantity resource.Quantity) patchRecord {
	return patchRecord{
		Op:    "add",
		Path:  fmt.Sprintf("/spec/containers/%d/resources/%s/%s", i, kind, resource),
		Value: quantity.String()}
}

func (s *AdmissionServer) getContainerPatch(pod v1.Pod, i int, annotationsPerContainer vpa_api_util.ContainerToAnnotationsMap, containerResources vpa_api_util.ContainerResources) ([]patchRecord, string) {
	var patches []patchRecord
	// Add empty resources object if missing
	if pod.Spec.Containers[i].Resources.Limits == nil &&
		pod.Spec.Containers[i].Resources.Requests == nil {
		patches = append(patches, getPatchInitializingEmptyResources(i))
	}

	annotations, found := annotationsPerContainer[pod.Spec.Containers[i].Name]
	if !found {
		annotations = make([]string, 0)
	}

	patches, annotations = appendPatchesAndAnnotations(patches, annotations, pod.Spec.Containers[i].Resources.Requests, i, containerResources.Requests, "requests", "request")
	patches, annotations = appendPatchesAndAnnotations(patches, annotations, pod.Spec.Containers[i].Resources.Limits, i, containerResources.Limits, "limits", "limit")
	// 对空资源进行填补和注释,获取Requests和Limits的信息

	updatesAnnotation := fmt.Sprintf("container %d: ", i) + strings.Join(annotations, ", ")
	// 更新注释
	return patches, updatesAnnotation
}

func appendPatchesAndAnnotations(patches []patchRecord, annotations []string, current v1.ResourceList, containerIndex int, resources v1.ResourceList, fieldName, resourceName string) ([]patchRecord, []string) {
	// Add empty object if it's missing and we're about to fill it.
	if current == nil && len(resources) > 0 {
		patches = append(patches, getPatchInitializingEmptyResourcesSubfield(containerIndex, fieldName))
		// 对空资源部分进行初始化，填补空缺
	}
	for resource, request := range resources {
		patches = append(patches, getAddResourceRequirementValuePatch(containerIndex, fieldName, resource, request))
		annotations = append(annotations, fmt.Sprintf("%s %s", resource, resourceName))
	}
	return patches, annotations
}

func parseVPA(raw []byte) (*vpa_types.VerticalPodAutoscaler, error) {
	vpa := vpa_types.VerticalPodAutoscaler{}
	if err := json.Unmarshal(raw, &vpa); err != nil {
		return nil, err
	}
	return &vpa, nil
}

var (
	possibleUpdateModes = map[vpa_types.UpdateMode]interface{}{
		vpa_types.UpdateModeOff:      struct{}{},
		vpa_types.UpdateModeInitial:  struct{}{},
		vpa_types.UpdateModeRecreate: struct{}{},
		vpa_types.UpdateModeAuto:     struct{}{},
	}

	possibleScalingModes = map[vpa_types.ContainerScalingMode]interface{}{
		vpa_types.ContainerScalingModeAuto: struct{}{},
		vpa_types.ContainerScalingModeOff:  struct{}{},
	}
)

func validateVPA(vpa *vpa_types.VerticalPodAutoscaler, isCreate bool) error {
	if vpa.Spec.UpdatePolicy != nil {
		mode := vpa.Spec.UpdatePolicy.UpdateMode
		if mode == nil {
			return fmt.Errorf("UpdateMode is required if UpdatePolicy is used")
		}
		if _, found := possibleUpdateModes[*mode]; !found {
			return fmt.Errorf("unexpected UpdateMode value %s", *mode)
		}
	}

	if vpa.Spec.ResourcePolicy != nil {
		for _, policy := range vpa.Spec.ResourcePolicy.ContainerPolicies {
			if policy.ContainerName == "" {
				return fmt.Errorf("ContainerPolicies.ContainerName is required")
			}
			mode := policy.Mode
			if mode != nil {
				if _, found := possibleScalingModes[*mode]; !found {
					return fmt.Errorf("unexpected Mode value %s", *mode)
				}
			}
			for resource, min := range policy.MinAllowed {
				max, found := policy.MaxAllowed[resource]
				if found && max.Cmp(min) < 0 {
					return fmt.Errorf("max resource for %v is lower than min", resource)
				}
			}
		}
	}

	if isCreate && vpa.Spec.TargetRef == nil {
		return fmt.Errorf("TargetRef is required. If you're using v1beta1 version of the API, please migrate to v1.")
	}

	return nil
}

func (s *AdmissionServer) getPatchesForVPADefaults(raw []byte, isCreate bool) ([]patchRecord, error) {
	vpa, err := parseVPA(raw)
	if err != nil {
		return nil, err
	}

	vpa, err = s.vpaPreProcessor.Process(vpa, isCreate)
	if err != nil {
		return nil, err
	}

	err = validateVPA(vpa, isCreate)
	if err != nil {
		return nil, err
	}

	klog.V(4).Infof("Processing vpa: %v", vpa)
	patches := []patchRecord{}
	if vpa.Spec.UpdatePolicy == nil {
		// Sets the default updatePolicy.
		defaultUpdateMode := vpa_types.UpdateModeAuto
		// 如果资源类型是vpa，则使用默认的更新策略
		patches = append(patches, patchRecord{
			Op:    "add",
			Path:  "/spec/updatePolicy",
			Value: vpa_types.PodUpdatePolicy{UpdateMode: &defaultUpdateMode}})
		// 对填补信息进行赋值
	}
	return patches, nil
}

func (s *AdmissionServer) admit(data []byte) (*v1beta1.AdmissionResponse, metrics_admission.AdmissionStatus, metrics_admission.AdmissionResource) {
	// we don't block the admission by default, even on unparsable JSON
	// 默认情况下，即使在无法解析的JSON上，我们也不会阻止访问
	response := v1beta1.AdmissionResponse{}
	// 访问的响应
	response.Allowed = true
	// 将响应设为允许状态

	ar := v1beta1.AdmissionReview{}
	// admission请求
	if err := json.Unmarshal(data, &ar); err != nil {
		// 如果json无法解析，则返回响应和错误信息，metrics_admission.Error, metrics_admission.Unknown两个参数告诉监控系统发生错误
		klog.Error(err)
		return &response, metrics_admission.Error, metrics_admission.Unknown
	}
	// The externalAdmissionHookConfiguration registered via selfRegistration
	// asks the kube-apiserver only to send admission requests regarding pods & VPA objects.
	// 请求kube-apiserver，要求其只发送有关pods和vpa对象的admission请求
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	vpaGroupResource := metav1.GroupResource{Group: "autoscaling.k8s.io", Resource: "verticalpodautoscalers"}

	var patches []patchRecord
	var err error
	resource := metrics_admission.Unknown

	admittedGroupResource := metav1.GroupResource{
		Group:    ar.Request.Resource.Group,
		Resource: ar.Request.Resource.Resource,
	}

	if ar.Request.Resource == podResource {
		// admission请求和从kube-apiserver获取到的pod的admission请求相同
		patches, err = s.getPatchesForPodResourceRequest(ar.Request.Object.Raw, ar.Request.Namespace)
		// 从pod的资源请求中获取填补信息patches
		resource = metrics_admission.Pod
		// 将资源类型赋值为pod
	} else if admittedGroupResource == vpaGroupResource {
		// admission请求和从kube-apiserver获取到的vpa的admission请求相同
		patches, err = s.getPatchesForVPADefaults(ar.Request.Object.Raw, ar.Request.Operation == v1beta1.Create)
		// 从vpa的资源请求中获取填补信息patches
		resource = metrics_admission.Vpa
		// 将资源类型赋值为vpa
		// we don't let in problematic VPA objects - late validation
		if err != nil {
			status := metav1.Status{}
			status.Status = "Failure"
			status.Message = err.Error()
			response.Result = &status
			response.Allowed = false
		}
	} else {
		patches, err = nil, fmt.Errorf("expected the resource to be one of: %v, %v", podResource, vpaGroupResource)
		// 如果两个资源都不是，则会输出报错
	}

	if err != nil {
		klog.Error(err)
		return &response, metrics_admission.Error, resource
	}

	if len(patches) > 0 {
		patch, err := json.Marshal(patches)
		// 解析patches
		if err != nil {
			klog.Errorf("Cannot marshal the patch %v: %v", patches, err)
			return &response, metrics_admission.Error, resource
		}
		patchType := v1beta1.PatchTypeJSONPatch
		response.PatchType = &patchType
		response.Patch = patch
		// 解析得到的patch赋值给response用于响应
		klog.V(4).Infof("Sending patches: %v", patches)
	}
	// 和metrics交互
	var status metrics_admission.AdmissionStatus
	if len(patches) > 0 {
		status = metrics_admission.Applied
	} else {
		status = metrics_admission.Skipped
	}
	if resource == metrics_admission.Pod {
		metrics_admission.OnAdmittedPod(status == metrics_admission.Applied)
	}

	return &response, status, resource
}

// Serve is a handler function of AdmissionServer
// handler的服务函数，用于启动AdmissionServer
func (s *AdmissionServer) Serve(w http.ResponseWriter, r *http.Request) {
	timer := metrics_admission.NewAdmissionLatency()
	// 更新监控时间
	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
			// 提取请求信息
		}
	}

	// verify the content type is accurate
	// 证明收到的请求是没问题的
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		klog.Errorf("contentType=%s, expect application/json", contentType)
		timer.Observe(metrics_admission.Error, metrics_admission.Unknown)
		return
	}

	reviewResponse, status, resource := s.admit(body)
	// admit函数从body中提取响应信息，辨别出是pod的响应还是vpa的响应，返回响应（里面存有patch信息），状态和资源类型
	ar := v1beta1.AdmissionReview{
		Response: reviewResponse,
	}
	// 进行回调

	resp, err := json.Marshal(ar)
	// json解析ar的信息
	if err != nil {
		klog.Error(err)
		timer.Observe(metrics_admission.Error, resource)
		// 进行监控，返回错误
		return
	}

	if _, err := w.Write(resp); err != nil {
		// 通过w将ar(resp)写入固定的路径中
		klog.Error(err)
		timer.Observe(metrics_admission.Error, resource)
		// 进行监控 返回错误
		return
	}

	timer.Observe(status, resource)
	// 进行监控
}
