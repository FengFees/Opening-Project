/*
Copyright 2017 The Kubernetes Authors.

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

package priority

import (
	"flag"
	"math"
	"sort"
	"time"

	apiv1 "k8s.io/api/core/v1"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_api_util "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
	"k8s.io/klog"
)

const (
	// Ignore change priority that is smaller than 10%.
	defaultUpdateThreshold = 0.10
	// Pods that live for at least that long can be evicted even if their
	// request is within the [MinRecommended...MaxRecommended] range.
	podLifetimeUpdateThreshold = time.Hour * 12
)

var (
	evictAfterOOMThreshold = flag.Duration("evict-after-oom-threshold", 10*time.Minute,
		`Evict pod that has only one container and it OOMed in less than
		evict-after-oom-threshold since start.`)
)

// UpdatePriorityCalculator is responsible for prioritizing updates on pods.
// It can returns a sorted list of pods in order of update priority.
// Update priority is proportional to fraction by which resources should be increased / decreased.
// i.e. pod with 10M current memory and recommendation 20M will have higher update priority
// than pod with 100M current memory and 150M recommendation (100% increase vs 50% increase)
type UpdatePriorityCalculator struct {
	resourcesPolicy         *vpa_types.PodResourcePolicy
	conditions              []vpa_types.VerticalPodAutoscalerCondition
	pods                    []podPriority
	config                  *UpdateConfig
	recommendationProcessor vpa_api_util.RecommendationProcessor
}

// UpdateConfig holds configuration for UpdatePriorityCalculator
type UpdateConfig struct {
	// MinChangePriority is the minimum change priority that will trigger a update.
	// TODO: should have separate for Mem and CPU?
	MinChangePriority float64
}

// NewUpdatePriorityCalculator creates new UpdatePriorityCalculator for the given resources policy and configuration.
// If the given policy is nil, there will be no policy restriction on update.
// If the given config is nil, default values are used.
// 更新优先级计算
func NewUpdatePriorityCalculator(policy *vpa_types.PodResourcePolicy,
	conditions []vpa_types.VerticalPodAutoscalerCondition,
	config *UpdateConfig,
	processor vpa_api_util.RecommendationProcessor) UpdatePriorityCalculator {
	if config == nil {
		config = &UpdateConfig{MinChangePriority: defaultUpdateThreshold}
	}
	return UpdatePriorityCalculator{resourcesPolicy: policy, conditions: conditions, config: config, recommendationProcessor: processor}
}

// AddPod adds pod to the UpdatePriorityCalculator.
// 增加pod到UpdatePriorityCalculator中
func (calc *UpdatePriorityCalculator) AddPod(pod *apiv1.Pod, recommendation *vpa_types.RecommendedPodResources, now time.Time) {
	processedRecommendation, _, err := calc.recommendationProcessor.Apply(recommendation, calc.resourcesPolicy, calc.conditions, pod)
	// 处理建议
	if err != nil {
		klog.V(2).Infof("cannot process recommendation for pod %s: %v", pod.Name, err)
		return
	}

	updatePriority := calc.getUpdatePriority(pod, processedRecommendation)
	// 根据建议 pod获取更新优先级

	quickOOM := false
	if len(pod.Status.ContainerStatuses) == 1 {
		terminationState := pod.Status.ContainerStatuses[0].LastTerminationState
		if terminationState.Terminated != nil &&
			terminationState.Terminated.Reason == "OOMKilled" &&
			terminationState.Terminated.FinishedAt.Time.Sub(terminationState.Terminated.StartedAt.Time) < *evictAfterOOMThreshold {
			quickOOM = true
			klog.V(2).Infof("quick OOM detected in pod %v", pod.Name)
		}
	}
	// OOM ？？？？？？

	// The update is allowed in following cases:
	// - the request is outside the recommended range for some container.
	// - the pod lives for at least 24h and the resource diff is >= MinChangePriority.
	// - there is only one container in a pod and it OOMed in less than evictAfterOOMThreshold

	// 在以下情况下允许更新：
	//-请求超出了某些容器的建议范围。
	//-pod至少生存24小时，并且资源差异> = MinChangePriority。
	//-pod中只有一个容器，并且它在小于evictAfterOOMThreshold的时间内进行了OOM操作
	if !updatePriority.outsideRecommendedRange && !quickOOM {
		if pod.Status.StartTime == nil {
			// TODO: Set proper condition on the VPA.
			klog.V(2).Infof("not updating pod %v, missing field pod.Status.StartTime", pod.Name)
			return
		}
		if now.Before(pod.Status.StartTime.Add(podLifetimeUpdateThreshold)) {
			klog.V(2).Infof("not updating a short-lived pod %v, request within recommended range", pod.Name)
			return
		}
		if updatePriority.resourceDiff < calc.config.MinChangePriority {
			klog.V(2).Infof("not updating pod %v, resource diff too low: %v", pod.Name, updatePriority)
			return
		}
	}
	klog.V(2).Infof("pod accepted for update %v with priority %v", pod.Name, updatePriority.resourceDiff)
	calc.pods = append(calc.pods, updatePriority)
	// 进行添加操作
}

// GetSortedPods returns a list of pods ordered by update priority (highest update priority first)
func (calc *UpdatePriorityCalculator) GetSortedPods(admission PodEvictionAdmission) []*apiv1.Pod {
	sort.Sort(byPriority(calc.pods))
	// 进行排序
	result := []*apiv1.Pod{}
	for _, podPrio := range calc.pods {
		if admission == nil || admission.Admit(podPrio.pod, podPrio.recommendation) {
			// 如果admission能够使得pod接纳该建议，则将结果添加
			result = append(result, podPrio.pod)
		} else {
			klog.V(2).Infof("pod removed from update queue by PodEvictionAdmission: %v", podPrio.pod.Name)
		}
	}

	return result
}

func (calc *UpdatePriorityCalculator) getUpdatePriority(pod *apiv1.Pod, recommendation *vpa_types.RecommendedPodResources) podPriority {
	outsideRecommendedRange := false
	scaleUp := false
	// Sum of requests over all containers, per resource type.
	totalRequestPerResource := make(map[apiv1.ResourceName]int64)
	// Sum of recommendations over all containers, per resource type.
	totalRecommendedPerResource := make(map[apiv1.ResourceName]int64)

	for _, podContainer := range pod.Spec.Containers {
		recommendedRequest := vpa_api_util.GetRecommendationForContainer(podContainer.Name, recommendation)
		// 获取推荐资源
		if recommendedRequest == nil {
			continue
		}
		for resourceName, recommended := range recommendedRequest.Target {
			totalRecommendedPerResource[resourceName] += recommended.MilliValue()
			lowerBound, hasLowerBound := recommendedRequest.LowerBound[resourceName]
			// 下限
			upperBound, hasUpperBound := recommendedRequest.UpperBound[resourceName]
			// 上限
			if request, hasRequest := podContainer.Resources.Requests[resourceName]; hasRequest {
				totalRequestPerResource[resourceName] += request.MilliValue()
				if recommended.MilliValue() > request.MilliValue() {
					scaleUp = true
				}
				if (hasLowerBound && request.Cmp(lowerBound) < 0) ||
					(hasUpperBound && request.Cmp(upperBound) > 0) {
					outsideRecommendedRange = true
				}
			} else {
				// Note: if the request is not specified, the container will use the
				// namespace default request. Currently we ignore it and treat such
				// containers as if they had 0 request. A more correct approach would
				// be to always calculate the 'effective' request.
				scaleUp = true
				outsideRecommendedRange = true
			}
		}
	}
	resourceDiff := 0.0
	for resource, totalRecommended := range totalRecommendedPerResource {
		totalRequest := math.Max(float64(totalRequestPerResource[resource]), 1.0)
		// 全部的资源数
		resourceDiff += math.Abs(totalRequest-float64(totalRecommended)) / totalRequest
		// 求资源率
	}
	return podPriority{
		pod:                     pod,
		outsideRecommendedRange: outsideRecommendedRange, // 外部建议范围
		scaleUp:                 scaleUp, // 是否扩容
		resourceDiff:            resourceDiff, // 资源率
		recommendation:          recommendation, // 建议
	}
}

type podPriority struct {
	pod *apiv1.Pod
	// Is any container outside of the recommended range.
	outsideRecommendedRange bool
	// Does any container want to grow.
	scaleUp bool
	// Relative difference between the total requested and total recommended resources.
	resourceDiff float64
	// Recommendation for pod
	recommendation *vpa_types.RecommendedPodResources
}

type byPriority []podPriority

func (list byPriority) Len() int {
	return len(list)
}
func (list byPriority) Swap(i, j int) {
	list[i], list[j] = list[j], list[i]
}

// Less implements reverse ordering by priority (highest priority first).
func (list byPriority) Less(i, j int) bool {
	// 1. If any container wants to grow, the pod takes precedence.
	// TODO: A better policy would be to prioritize scaling down when
	// (a) the pod is pending
	// (b) there is general resource shortage
	// and prioritize scaling up otherwise.
	if list[i].scaleUp != list[j].scaleUp {
		return list[i].scaleUp
	}
	// 2. A pod with larger value of resourceDiff takes precedence.
	return list[i].resourceDiff > list[j].resourceDiff
}
