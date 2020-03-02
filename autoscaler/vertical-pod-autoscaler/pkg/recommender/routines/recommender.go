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

package routines

import (
	"context"
	"flag"
	"time"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_clientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	vpa_api "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/typed/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/checkpoint"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/logic"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	metrics_recommender "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/recommender"
	vpa_utils "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
)

const (
	// 定义了多久对过期的AggregateContainerStates进行垃圾回收
	AggregateContainerStateGCInterval = 1 * time.Hour
)

var (
	// 从recommender主循环开始到写下checkpoints的超时时间
	checkpointsWriteTimeout = flag.Duration("checkpoints-timeout", time.Minute, `Timeout for writing checkpoints since the start of the recommender's main loop`)
	// 每次主循环写的checkpoints最少数量
	minCheckpointsPerRun    = flag.Int("min-checkpoints", 10, "Minimum number of checkpoints to write per recommender's main loop")
	// 如果设为true，则只记录有关联VPA的pods
	memorySaver             = flag.Bool("memory-saver", false, `If true, only track pods which have an associated VPA`)
)

// Recommender根据从metrics api周期性得到的数据为指定容器建议资源
type Recommender interface {
	// RunOnce实现recommender在一次迭代中所需的工作
	// 随后更新VPA对象中的推荐值
	RunOnce()
	// GetClusterState 返回Recommender使用的ClusterState
	GetClusterState() *model.ClusterState
	// GetClusterStateFeeder 返回Recommender使用的ClusterStateFeeder
	GetClusterStateFeeder() input.ClusterStateFeeder
	// UpdateVPAs 计算推荐值并把VPA状态更新送给API Server
	UpdateVPAs()
	// MaintainCheckpoints将当前checkpoints存进API Server并回收旧值
	// 如果需要写更多的checkpoints，则至少写minCheckpoints个，直到写完或ctx允许
	MaintainCheckpoints(ctx context.Context, minCheckpoints int)
	// GarbageCollect移除旧的AggregateCollectionStates
	GarbageCollect()
}

type recommender struct {
	clusterState                  *model.ClusterState
	clusterStateFeeder            input.ClusterStateFeeder
	checkpointWriter              checkpoint.CheckpointWriter
	checkpointsGCInterval         time.Duration
	lastCheckpointGC              time.Time
	vpaClient                     vpa_api.VerticalPodAutoscalersGetter
	podResourceRecommender        logic.PodResourceRecommender
	useCheckpoints                bool
	lastAggregateContainerStateGC time.Time
}

func (r *recommender) GetClusterState() *model.ClusterState {
	return r.clusterState
}

func (r *recommender) GetClusterStateFeeder() input.ClusterStateFeeder {
	return r.clusterStateFeeder
}

// 更新VPA中的建议 该vpa为CRD对象
func (r *recommender) UpdateVPAs() {
	// 从metrics中获取数据对象
	cnt := metrics_recommender.NewObjectCounter()
	// 持续观察
	defer cnt.Observe()

	for _, observedVpa := range r.clusterState.ObservedVpas {
		key := model.VpaID{
			Namespace: observedVpa.Namespace,
			VpaName:   observedVpa.Name}

		vpa, found := r.clusterState.Vpas[key]
		if !found {
			continue
		}
		resources := r.podResourceRecommender.GetRecommendedPodResources(GetContainerNameToAggregateStateMap(vpa))
		had := vpa.HasRecommendation()
		// vap执行自身的更新建议函数 注意是Recommendation！
		vpa.UpdateRecommendation(getCappedRecommendation(vpa.ID, resources, observedVpa.Spec.ResourcePolicy))
		if vpa.HasRecommendation() && !had {
			metrics_recommender.ObserveRecommendationLatency(vpa.Created)
		}
		hasMatchingPods := r.clusterState.VpasWithMatchingPods[vpa.ID]
		vpa.UpdateConditions(hasMatchingPods)
		if err := r.clusterState.RecordRecommendation(vpa, time.Now()); err != nil {
			klog.Warningf("%v", err)
			klog.V(4).Infof("VPA dump")
			klog.V(4).Infof("%+v", vpa)
			klog.V(4).Infof("HasMatchingPods: %v", hasMatchingPods)
			pods := r.clusterState.GetMatchingPods(vpa)
			klog.V(4).Infof("MatchingPods: %+v", pods)
			if len(pods) > 0 != hasMatchingPods {
				klog.Errorf("Aggregated states and matching pods disagree for vpa %v/%v", vpa.ID.Namespace, vpa.ID.VpaName)
			}
		}
		cnt.Add(vpa)

		_, err := vpa_utils.UpdateVpaStatusIfNeeded(
			r.vpaClient.VerticalPodAutoscalers(vpa.ID.Namespace), vpa.ID.VpaName, vpa.AsStatus(), &observedVpa.Status)
		if err != nil {
			klog.Errorf(
				"Cannot update VPA %v object. Reason: %+v", vpa.ID.VpaName, err)
		}
	}
}

// getCappedRecommendation creates a recommendation based on recommended pod
// resources, setting the UncappedTarget to the calculated recommended target
// and if necessary, capping the Target, LowerBound and UpperBound according
// to the ResourcePolicy.
// getCappedRecommendation基于推荐的pod资源创建一个推荐值，并将UncappedTarget设为计算到的
// 推荐值。如果必要，根据ResourcePolicy来计算上界和下界，从而封盖目标。
func getCappedRecommendation(vpaID model.VpaID, resources logic.RecommendedPodResources,
	policy *vpa_types.PodResourcePolicy) *vpa_types.RecommendedPodResources {
	containerResources := make([]vpa_types.RecommendedContainerResources, 0, len(resources))
	for containerName, res := range resources {
		containerResources = append(containerResources, vpa_types.RecommendedContainerResources{
			ContainerName:  containerName,
			Target:         model.ResourcesAsResourceList(res.Target),
			LowerBound:     model.ResourcesAsResourceList(res.LowerBound),
			UpperBound:     model.ResourcesAsResourceList(res.UpperBound),
			UncappedTarget: model.ResourcesAsResourceList(res.Target),
		})
	}
	recommendation := &vpa_types.RecommendedPodResources{containerResources}
	cappedRecommendation, err := vpa_utils.ApplyVPAPolicy(recommendation, policy)
	if err != nil {
		klog.Errorf("Failed to apply policy for VPA %v/%v: %v", vpaID.Namespace, vpaID.VpaName, err)
		return recommendation
	}
	return cappedRecommendation
}

func (r *recommender) MaintainCheckpoints(ctx context.Context, minCheckpointsPerRun int) {
	now := time.Now()
	if r.useCheckpoints {
		if err := r.checkpointWriter.StoreCheckpoints(ctx, now, minCheckpointsPerRun); err != nil {
			klog.Warningf("Failed to store checkpoints. Reason: %+v", err)
		}
		if time.Now().Sub(r.lastCheckpointGC) > r.checkpointsGCInterval {
			r.lastCheckpointGC = now
			r.clusterStateFeeder.GarbageCollectCheckpoints()
		}
	}

}

func (r *recommender) GarbageCollect() {
	gcTime := time.Now()
	if gcTime.Sub(r.lastAggregateContainerStateGC) > AggregateContainerStateGCInterval {
		r.clusterState.GarbageCollectAggregateCollectionStates(gcTime)
		r.lastAggregateContainerStateGC = gcTime
	}
}

// RunOnce实现recommender在一次迭代中所需的工作
// 随后更新VPA对象中的推荐值
func (r *recommender) RunOnce() {
	// 新建一个timer来测量计算的执行时间
	// 包括该次RunOnce的总时间和RunOnce中每一步的时间
	timer := metrics_recommender.NewExecutionTimer()
	defer timer.ObserveTotal()

	// 为当前执行设置写checkpoints的超时时间
	ctx := context.Background()
	ctx, cancelFunc := context.WithDeadline(ctx, time.Now().Add(*checkpointsWriteTimeout))
	defer cancelFunc()

	klog.V(3).Infof("Recommender Run")

	// 用VPAs的当前状态更新clusterState
	r.clusterStateFeeder.LoadVPAs()
	timer.ObserveStep("LoadVPAs")

	// 用当前Pods和它们的容器的规格更新clusterState
	r.clusterStateFeeder.LoadPods()
	timer.ObserveStep("LoadPods")

	// 用当前容器的使用情况更新clusterState
	r.clusterStateFeeder.LoadRealTimeMetrics()
	timer.ObserveStep("LoadMetrics")
	klog.V(3).Infof("ClusterState is tracking %v PodStates and %v VPAs", len(r.clusterState.Pods), len(r.clusterState.Vpas))

	// 计算推荐值并把VPA状态更新送给API Server
	r.UpdateVPAs()
	timer.ObserveStep("UpdateVPAs")

	// 将当前checkpoints存进API Server并回收旧值
	// 如果需要写更多的checkpoints，则至少写minCheckpoints个，直到写完或ctx允许
	r.MaintainCheckpoints(ctx, *minCheckpointsPerRun)
	timer.ObserveStep("MaintainCheckpoints")

	// 移除没有匹配VPA的历史checkpoints
	r.GarbageCollect()
	timer.ObserveStep("GarbageCollect")
	klog.V(3).Infof("ClusterState is tracking %d aggregated container states", r.clusterState.StateMapSize())
}

// RecommenderFactory用来创建Recommender实例
type RecommenderFactory struct {
	ClusterState *model.ClusterState

	ClusterStateFeeder     input.ClusterStateFeeder
	CheckpointWriter       checkpoint.CheckpointWriter
	PodResourceRecommender logic.PodResourceRecommender
	VpaClient              vpa_api.VerticalPodAutoscalersGetter

	CheckpointsGCInterval time.Duration
	UseCheckpoints        bool
}

// Make 创建一个新的recommender实例
// 可以为容器提供连续的资源推荐
func (c RecommenderFactory) Make() Recommender {
	recommender := &recommender{
		clusterState:                  c.ClusterState,
		clusterStateFeeder:            c.ClusterStateFeeder,
		checkpointWriter:              c.CheckpointWriter,
		checkpointsGCInterval:         c.CheckpointsGCInterval,
		useCheckpoints:                c.UseCheckpoints,
		vpaClient:                     c.VpaClient,
		podResourceRecommender:        c.PodResourceRecommender,
		lastAggregateContainerStateGC: time.Now(),
		lastCheckpointGC:              time.Now(),
	}
	klog.V(3).Infof("New Recommender created %+v", recommender)
	return recommender
}


// 创建一个新的recommender实例，自动创建相关依赖。
// 不再建议使用，建议使用RecommenderFactory
func NewRecommender(config *rest.Config, checkpointsGCInterval time.Duration, useCheckpoints bool) Recommender {
	clusterState := model.NewClusterState()
	return RecommenderFactory{
		ClusterState:           clusterState,
		ClusterStateFeeder:     input.NewClusterStateFeeder(config, clusterState, *memorySaver),
		CheckpointWriter:       checkpoint.NewCheckpointWriter(clusterState, vpa_clientset.NewForConfigOrDie(config).AutoscalingV1()),
		VpaClient:              vpa_clientset.NewForConfigOrDie(config).AutoscalingV1(),
		PodResourceRecommender: logic.CreatePodResourceRecommender(),
		CheckpointsGCInterval:  checkpointsGCInterval,
		UseCheckpoints:         useCheckpoints,
	}.Make()
}
