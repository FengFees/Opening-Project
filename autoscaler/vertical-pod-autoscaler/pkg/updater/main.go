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

package main

import (
	"context"
	"flag"
	"time"

	"k8s.io/autoscaler/vertical-pod-autoscaler/common"
	vpa_clientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/target"
	updater "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/updater/logic"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/limitrange"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics"
	metrics_updater "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/updater"
	vpa_api_util "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
	"k8s.io/client-go/informers"
	kube_client "k8s.io/client-go/kubernetes"
	kube_restclient "k8s.io/client-go/rest"
	kube_flag "k8s.io/component-base/cli/flag"
	"k8s.io/klog"
)

var (
	updaterInterval = flag.Duration("updater-interval", 1*time.Minute,
		`How often updater should run`)
	// 启动频率

	minReplicas = flag.Int("min-replicas", 2,
		`Minimum number of replicas to perform update`)
	// 最少的replicas数量，执行更新

	evictionToleranceFraction = flag.Float64("eviction-tolerance", 0.5,
		`Fraction of replica count that can be evicted for update, if more than one pod can be evicted.`)
	// 驱逐临界线，一旦超过这个线（分数），这个pod将会被驱逐

	evictionRateLimit = flag.Float64("eviction-rate-limit", -1,
		`Number of pods that can be evicted per seconds. A rate limit set to 0 or -1 will disable
		the rate limiter.`)
	// 每秒能够被驱逐的pod数量

	evictionRateBurst = flag.Int("eviction-rate-burst", 1, `Burst of pods that can be evicted.`)
	// 能够被驱逐pod的并发上限

	address = flag.String("address", ":8943", "The address to expose Prometheus metrics.")
	// 普罗米修斯地址
)

const (
	defaultResyncPeriod time.Duration = 10 * time.Minute
)

func main() {
	// 第一步：打印日志信息
	klog.InitFlags(nil)
	kube_flag.InitFlags()
	klog.V(1).Infof("Vertical Pod Autoscaler %s Updater", common.VerticalPodAutoscalerVersion)

	// 第二步：启动普罗米修斯的监控和healthCheck
	// 该步骤类似于控制器ac的第二步
	healthCheck := metrics.NewHealthCheck(*updaterInterval*5, true)
	metrics.Initialize(*address, healthCheck)
	metrics_updater.Register()

	// 第三步：获取集群信息和创建k8s vpa客户端，创建informer工厂
	config, err := kube_restclient.InClusterConfig()
	if err != nil {
		klog.Fatalf("Failed to build Kubernetes client : fail to create config: %v", err)
	}
	kubeClient := kube_client.NewForConfigOrDie(config)
	vpaClient := vpa_clientset.NewForConfigOrDie(config)

	//informer工厂，调用NewSharedInformerFactory()接口
	//疑问一：调用informers作用
	//答：创建一个名为 SharedInformerFactory 的单例工厂，因为每个Informer都会与Api Server维持一个watch长连接。
	factory := informers.NewSharedInformerFactory(kubeClient, defaultResyncPeriod)
	//sharedIndexInformer 是一个共享的 Informer 框架
	//vpa只需要提供一个模板类（比如 deploymentInformer ），便可以创建一个符合自己需求的特定 Informer。

	// 第四步：target
	// 疑问二：target是个什么？
	// 答： target所要做的事情就是通过discoveryClient来个API Server交互获得scale扩容的信息
	// 返回的vpaTargetSelectorFetcher结构体：
	// &vpaTargetSelectorFetcher{
	//      scaleNamespacer: scaleNamespacer,
	//      mapper:          mapper,
	//      informersMap:    informersMap,
	//   }
	// scaleNamespacer：扩容命名空间
	// mapper：discovery information
	// informersMap：七个资源类型的informer实例
	targetSelectorFetcher := target.NewVpaTargetSelectorFetcher(config, kubeClient, factory)

	// 第五步：创建计算限制资源类型
	var limitRangeCalculator limitrange.LimitRangeCalculator
	limitRangeCalculator, err = limitrange.NewLimitsRangeCalculator(factory)
	// 通过factory的sharedIndexInformer工厂来实例化limitrange这个资源对象，通过该资源对象获取API Server中的资源计算限制信息
	if err != nil {
		klog.Errorf("Failed to create limitRangeCalculator, falling back to not checking limits. Error message: %s", err)
		limitRangeCalculator = limitrange.NewNoopLimitsCalculator()
	}


	// TODO: use SharedInformerFactory in updater
	// 第六步：通过SharedInformerFactory创建updater资源类型（关键步骤）
	updater, err := updater.NewUpdater(kubeClient, vpaClient, *minReplicas, *evictionRateLimit, *evictionRateBurst, *evictionToleranceFraction, vpa_api_util.NewCappingRecommendationProcessor(limitRangeCalculator), nil, targetSelectorFetcher)
	if err != nil {
		klog.Fatalf("Failed to create updater: %v", err)
	}

	// 第七步：迭代时间 进行更新
	ticker := time.Tick(*updaterInterval)
	for range ticker {
		ctx, cancel := context.WithTimeout(context.Background(), *updaterInterval)
		defer cancel()
		updater.RunOnce(ctx)
		// 整个循环中单个迭代
		healthCheck.UpdateLastActivity()
		// 更新healthCheck
	}
}
