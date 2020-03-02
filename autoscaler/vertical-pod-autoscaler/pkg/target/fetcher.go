/*
Copyright 2019 The Kubernetes Authors.

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

package target

import (
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	batchv1beta1 "k8s.io/api/batch/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/client-go/discovery"
	cacheddiscovery "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	kube_client "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/scale"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

const (
	discoveryResetPeriod time.Duration = 5 * time.Minute
)

// VpaTargetSelectorFetcher gets a labelSelector used to gather Pods controlled by the given VPA.
// VpaTargetSelectorFetcher获取一个labelSelector，用于收集由给定VPA控制的Pods。
type VpaTargetSelectorFetcher interface {
	// Fetch returns a labelSelector used to gather Pods controlled by the given VPA.
	// If error is nil, the returned labelSelector is not nil.
	Fetch(vpa *vpa_types.VerticalPodAutoscaler) (labels.Selector, error)
}

type wellKnownController string

const (
	daemonSet             wellKnownController = "DaemonSet"
	deployment            wellKnownController = "Deployment"
	replicaSet            wellKnownController = "ReplicaSet"
	statefulSet           wellKnownController = "StatefulSet"
	replicationController wellKnownController = "ReplicationController"
	job                   wellKnownController = "Job"
	cronJob               wellKnownController = "CronJob"
)

// NewVpaTargetSelectorFetcher returns new instance of VpaTargetSelectorFetcher
// NewVpaTargetSelectorFetcher返回VpaTargetSelectorFetcher的新实例
func NewVpaTargetSelectorFetcher(config *rest.Config, kubeClient kube_client.Interface, factory informers.SharedInformerFactory) VpaTargetSelectorFetcher {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	// NewDiscoveryClientForConfig为给定的配置创建一个新的DiscoveryClient。 该客户端可用于发现API Server中受支持的资源。

	if err != nil {
		klog.Fatalf("Could not create discoveryClient: %v", err)
	}

	resolver := scale.NewDiscoveryScaleKindResolver(discoveryClient)
	// 疑问一： scale.NewDiscoveryScaleKindResolver作用?返回的resolver是个什么？
	// 答：NewDiscoveryScaleKindResolver创建一个新的ScaleKindResolver，
	// 它使用来自给定Disovery客户端的信息来为不同资源解析正确的Scale GroupVersionKind。
	// 返回的是cachedScaleKindResolver结构体，里面的cache存储Scale GroupVersionKind类型的资源。

	restClient := kubeClient.CoreV1().RESTClient()
	// 通过k8s客户端获取rest客户端
	cachedDiscoveryClient := cacheddiscovery.NewMemCacheClient(discoveryClient)
	// 疑问二：缓存cache客户端？
	// 答：NewMemCacheClient创建一个新的CachedDiscoveryInterface，它将discoveryClient中的discovery information缓存在内存cache中，
	// 如果定期调用invalidate，它将保持最新状态。（官方）

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscoveryClient)
	// NewDeferredDiscoveryRESTMapper返回DeferredDiscoveryRESTMapper
	// 它将延迟查询给以提供的客户端，以获取用于进行REST映射的discovery information。（官方）
	// 这里是把cachedDiscoveryClient中的缓存信息放入这个特定mapper中，特定是因为这个mapper中的信息是通过客户端延迟查询且通过rest映射得到的

	go wait.Until(func() {
		mapper.Reset()
	}, discoveryResetPeriod, make(chan struct{}))
	// 协程不断更新mapper的信息

	informersMap := map[wellKnownController]cache.SharedIndexInformer{
		daemonSet:             factory.Apps().V1().DaemonSets().Informer(),
		deployment:            factory.Apps().V1().Deployments().Informer(),
		replicaSet:            factory.Apps().V1().ReplicaSets().Informer(),
		statefulSet:           factory.Apps().V1().StatefulSets().Informer(),
		replicationController: factory.Core().V1().ReplicationControllers().Informer(),
		job:                   factory.Batch().V1().Jobs().Informer(),
		cronJob:               factory.Batch().V1beta1().CronJobs().Informer(),
	}
	// 七个资源类型通过cache.SharedIndexInformer来实现Informer实例，在map storage中存储

	for kind, informer := range informersMap {
		stopCh := make(chan struct{})
		go informer.Run(stopCh)
		// 不断启动informersMap中实例化的informer
		synced := cache.WaitForCacheSync(stopCh, informer.HasSynced)
		// 等待所有已经启动的 Informer 的 Cache 同步完成，同步全量对象
		// WaitForCacheSync等待缓存填充。 如果成功，则返回true；如果控制器应关闭，则返回false
		if !synced {
			klog.Fatalf("Could not sync cache for %s: %v", kind, err)
		} else {
			klog.Infof("Initial sync of %s completed", kind)
		}
	}
	// 上面的循环就是一个不断启动informer和不断更新同步缓存的一个过程

	scaleNamespacer := scale.New(restClient, mapper, dynamic.LegacyAPIPathResolverFunc, resolver)
	// scale.New使用给定的客户端来创建新的ScalesGetter进行请求。
	// scaleNamespacer是一个scaleClient结构体：
	// scaleClient{
	//		mapper: mapper,
	//		apiPathResolverFunc: resolver,
	//		scaleKindResolver:   scaleKindResolver,
	//		clientBase:          baseClient,
	//	}

	return &vpaTargetSelectorFetcher{
		scaleNamespacer: scaleNamespacer,
		mapper:          mapper,
		informersMap:    informersMap,
	}

}

// vpaTargetSelectorFetcher implements VpaTargetSelectorFetcher interface
// by querying API server for the controller pointed by VPA's targetRef
type vpaTargetSelectorFetcher struct {
	scaleNamespacer scale.ScalesGetter
	mapper          apimeta.RESTMapper
	informersMap    map[wellKnownController]cache.SharedIndexInformer
}

func (f *vpaTargetSelectorFetcher) Fetch(vpa *vpa_types.VerticalPodAutoscaler) (labels.Selector, error) {
	if vpa.Spec.TargetRef == nil {
		return nil, fmt.Errorf("targetRef not defined. If this is a v1beta1 object switch to v1beta2.")
	}
	kind := wellKnownController(vpa.Spec.TargetRef.Kind)
	informer, exists := f.informersMap[kind]
	if exists {
		return getLabelSelector(informer, vpa.Spec.TargetRef.Kind, vpa.Namespace, vpa.Spec.TargetRef.Name)
	}

	// not on a list of known controllers, use scale sub-resource
	// TODO: cache response
	groupVersion, err := schema.ParseGroupVersion(vpa.Spec.TargetRef.APIVersion)
	if err != nil {
		return nil, err
	}
	groupKind := schema.GroupKind{
		Group: groupVersion.Group,
		Kind:  vpa.Spec.TargetRef.Kind,
	}

	selector, err := f.getLabelSelectorFromResource(groupKind, vpa.Namespace, vpa.Spec.TargetRef.Name)
	if err != nil {
		return nil, fmt.Errorf("Unhandled targetRef %s / %s / %s, last error %v",
			vpa.Spec.TargetRef.APIVersion, vpa.Spec.TargetRef.Kind, vpa.Spec.TargetRef.Name, err)
	}
	return selector, nil
}

func getLabelSelector(informer cache.SharedIndexInformer, kind, namespace, name string) (labels.Selector, error) {
	obj, exists, err := informer.GetStore().GetByKey(namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("%s %s/%s does not exist", kind, namespace, name)
	}
	switch obj.(type) {
	case (*appsv1.DaemonSet):
		apiObj, ok := obj.(*appsv1.DaemonSet)
		if !ok {
			return nil, fmt.Errorf("Failed to parse %s %s/%s", kind, namespace, name)
		}
		return metav1.LabelSelectorAsSelector(apiObj.Spec.Selector)
	case (*appsv1.Deployment):
		apiObj, ok := obj.(*appsv1.Deployment)
		if !ok {
			return nil, fmt.Errorf("Failed to parse %s %s/%s", kind, namespace, name)
		}
		return metav1.LabelSelectorAsSelector(apiObj.Spec.Selector)
	case (*appsv1.StatefulSet):
		apiObj, ok := obj.(*appsv1.StatefulSet)
		if !ok {
			return nil, fmt.Errorf("Failed to parse %s %s/%s", kind, namespace, name)
		}
		return metav1.LabelSelectorAsSelector(apiObj.Spec.Selector)
	case (*appsv1.ReplicaSet):
		apiObj, ok := obj.(*appsv1.ReplicaSet)
		if !ok {
			return nil, fmt.Errorf("Failed to parse %s %s/%s", kind, namespace, name)
		}
		return metav1.LabelSelectorAsSelector(apiObj.Spec.Selector)
	case (*batchv1.Job):
		apiObj, ok := obj.(*batchv1.Job)
		if !ok {
			return nil, fmt.Errorf("Failed to parse %s %s/%s", kind, namespace, name)
		}
		return metav1.LabelSelectorAsSelector(apiObj.Spec.Selector)
	case (*batchv1beta1.CronJob):
		apiObj, ok := obj.(*batchv1beta1.CronJob)
		if !ok {
			return nil, fmt.Errorf("Failed to parse %s %s/%s", kind, namespace, name)
		}
		return metav1.LabelSelectorAsSelector(metav1.SetAsLabelSelector(apiObj.Spec.JobTemplate.Spec.Template.Labels))
	case (*corev1.ReplicationController):
		apiObj, ok := obj.(*corev1.ReplicationController)
		if !ok {
			return nil, fmt.Errorf("Failed to parse %s %s/%s", kind, namespace, name)
		}
		return metav1.LabelSelectorAsSelector(metav1.SetAsLabelSelector(apiObj.Spec.Selector))
	}
	return nil, fmt.Errorf("Don't know how to read label seletor")
}

func (f *vpaTargetSelectorFetcher) getLabelSelectorFromResource(
	groupKind schema.GroupKind, namespace, name string,
) (labels.Selector, error) {
	mappings, err := f.mapper.RESTMappings(groupKind)
	if err != nil {
		return nil, err
	}

	var lastError error
	for _, mapping := range mappings {
		groupResource := mapping.Resource.GroupResource()
		scale, err := f.scaleNamespacer.Scales(namespace).Get(groupResource, name)
		if err == nil {
			selector, err := labels.Parse(scale.Status.Selector)
			if err != nil {
				return nil, err
			}
			return selector, nil
		}
		lastError = err
	}

	// nothing found, apparently the resource does support scale (or we lack RBAC)
	return nil, lastError
}
