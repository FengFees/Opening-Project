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
	"fmt"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_lister "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/target"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/limitrange"
	vpa_api_util "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
	"k8s.io/klog"
)

// RecommendationProvider gets current recommendation, annotations and vpaName for the given pod.
// RecommendationProvider获取给定pod的当前recommendation, annotations and vpaName。
type RecommendationProvider interface {
	GetContainersResourcesForPod(pod *core.Pod) ([]vpa_api_util.ContainerResources, vpa_api_util.ContainerToAnnotationsMap, string, error)
}

type recommendationProvider struct {
	limitsRangeCalculator   limitrange.LimitRangeCalculator
	recommendationProcessor vpa_api_util.RecommendationProcessor
	selectorFetcher         target.VpaTargetSelectorFetcher
	vpaLister               vpa_lister.VerticalPodAutoscalerLister
}

// NewRecommendationProvider constructs the recommendation provider that list VPAs and can be used to determine recommendations for pods.
// NewRecommendationProvider构造列出VPA的recommendation提供者，可用于确定Pod的recommendation。
func NewRecommendationProvider(calculator limitrange.LimitRangeCalculator, recommendationProcessor vpa_api_util.RecommendationProcessor,
	selectorFetcher target.VpaTargetSelectorFetcher, vpaLister vpa_lister.VerticalPodAutoscalerLister) *recommendationProvider {
	return &recommendationProvider{
		limitsRangeCalculator:   calculator,
		recommendationProcessor: recommendationProcessor,
		selectorFetcher:         selectorFetcher,
		vpaLister:               vpaLister,
	}
}

// GetContainersResources returns the recommended resources for each container in the given pod in the same order they are specified in the pod.Spec.
// GetContainersResources按照给定pod.Spec中指定的顺序返回给定pod中每个容器的recommended资源。
func GetContainersResources(pod *core.Pod, podRecommendation vpa_types.RecommendedPodResources, limitRange *core.LimitRangeItem,
	annotations vpa_api_util.ContainerToAnnotationsMap) []vpa_api_util.ContainerResources {
	resources := make([]vpa_api_util.ContainerResources, len(pod.Spec.Containers))
	for i, container := range pod.Spec.Containers {
		recommendation := vpa_api_util.GetRecommendationForContainer(container.Name, &podRecommendation)
		// 从给定的容器中获得匹配的recommendation
		if recommendation == nil {
			klog.V(2).Infof("no matching recommendation found for container %s", container.Name)
			continue
		}
		resources[i].Requests = recommendation.Target
		defaultLimit := core.ResourceList{}
		// 获取资源
		if limitRange != nil {
			defaultLimit = limitRange.Default
		}
		// 赋值默认限制
		proportionalLimits, limitAnnotations := vpa_api_util.GetProportionalLimit(container.Resources.Limits, container.Resources.Requests, recommendation.Target, defaultLimit)
		// 获得相应比例下对内存和cpu的限制
		if proportionalLimits != nil {
			resources[i].Limits = proportionalLimits
			// 进行存储
			if len(limitAnnotations) > 0 {
				annotations[container.Name] = append(annotations[container.Name], limitAnnotations...)
			}
		}
	}
	return resources
}

func (p *recommendationProvider) getMatchingVPA(pod *core.Pod) *vpa_types.VerticalPodAutoscaler {
	configs, err := p.vpaLister.VerticalPodAutoscalers(pod.Namespace).List(labels.Everything())
	// 获取所有vpa的list，写入configs配置变量中
	if err != nil {
		klog.Errorf("failed to get vpa configs: %v", err)
		return nil
	}
	onConfigs := make([]*vpa_api_util.VpaWithSelector, 0)
	// 循环获得configs中的vpa，对这些vpa进行筛选
	for _, vpaConfig := range configs {
		if vpa_api_util.GetUpdateMode(vpaConfig) == vpa_types.UpdateModeOff {
			// 如果该vpa的更新模式关闭了，则选择下一个vpa
			continue
		}
		selector, err := p.selectorFetcher.Fetch(vpaConfig)
		// 抓取该vpa的选择器，放入selector中
		if err != nil {
			klog.V(3).Infof("skipping VPA object %v because we cannot fetch selector: %s", vpaConfig.Name, err)
			continue
		}
		onConfigs = append(onConfigs, &vpa_api_util.VpaWithSelector{
			Vpa:      vpaConfig,
			Selector: selector,
		})
	}
	klog.V(2).Infof("Let's choose from %d configs for pod %s/%s", len(onConfigs), pod.Namespace, pod.Name)
	result := vpa_api_util.GetControllingVPAForPod(pod, onConfigs)
	// 从config配置中选择和pod匹配的创建时间最早vpa
	if result != nil {
		return result.Vpa
	}
	return nil
}

// GetContainersResourcesForPod returns recommended request for a given pod, annotations and name of controlling VPA.
// The returned slice corresponds 1-1 to containers in the Pod.
// 更新对于指定pod的recommended推荐需求
func (p *recommendationProvider) GetContainersResourcesForPod(pod *core.Pod) ([]vpa_api_util.ContainerResources, vpa_api_util.ContainerToAnnotationsMap, string, error) {
	klog.V(2).Infof("updating requirements for pod %s.", pod.Name)
	vpaConfig := p.getMatchingVPA(pod)
	// 一. 获取指定的vpa(创建时间最早的)，该vpa更新状态未设置为off并且和截取到的pod创建信息匹配
	if vpaConfig == nil {
		klog.V(2).Infof("no matching VPA found for pod %s", pod.Name)
		return nil, nil, "", nil
		// 若不匹配，则返回无。
	}

	var annotations vpa_api_util.ContainerToAnnotationsMap
	recommendedPodResources := &vpa_types.RecommendedPodResources{}

	if vpaConfig.Status.Recommendation != nil {
		var err error
		recommendedPodResources, annotations, err = p.recommendationProcessor.Apply(vpaConfig.Status.Recommendation, vpaConfig.Spec.ResourcePolicy, vpaConfig.Status.Conditions, pod)
		// 二. 后处理recommendation
		if err != nil {
			klog.V(2).Infof("cannot process recommendation for pod %s", pod.Name)
			return nil, annotations, vpaConfig.Name, err
		}
	}
	containerLimitRange, err := p.limitsRangeCalculator.GetContainerLimitRangeItem(pod.Namespace)
	// 三. 获取容器运行时的限制范围
	if err != nil {
		return nil, nil, "", fmt.Errorf("error getting containerLimitRange: %s", err)
	}
	containerResources := GetContainersResources(pod, *recommendedPodResources, containerLimitRange, annotations)
	// 四. 获取容器资源，返回的resources保存了每个容器对内存和cpu的限制信息
	return containerResources, annotations, vpaConfig.Name, nil
}
