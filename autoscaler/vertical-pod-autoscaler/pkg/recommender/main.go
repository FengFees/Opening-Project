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
	"flag"
	"time"

	"k8s.io/autoscaler/vertical-pod-autoscaler/common"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/history"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/routines"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics"
	metrics_quality "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/quality"
	metrics_recommender "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/recommender"
	"k8s.io/client-go/rest"
	kube_flag "k8s.io/component-base/cli/flag"
	"k8s.io/klog"
)

var (
	metricsFetcherInterval = flag.Duration("recommender-interval", 1*time.Minute, `How often metrics should be fetched`)
	checkpointsGCInterval  = flag.Duration("checkpoints-gc-interval", 10*time.Minute, `How often orphaned checkpoints should be garbage collected`)
	prometheusAddress      = flag.String("prometheus-address", "", `Where to reach for Prometheus metrics`)
	prometheusJobName      = flag.String("prometheus-cadvisor-job-name", "kubernetes-cadvisor", `Name of the prometheus job name which scrapes the cAdvisor metrics`)
	address                = flag.String("address", ":8942", "The address to expose Prometheus metrics.")
	kubeApiQps             = flag.Float64("kube-api-qps", 5.0, `QPS limit when making requests to Kubernetes apiserver`)
	kubeApiBurst           = flag.Float64("kube-api-burst", 10.0, `QPS burst limit when making requests to Kubernetes apiserver`)

	// 支持的存储模式有：prometheus和checkpoint(默认值)
	storage = flag.String("storage", "", `Specifies storage mode. Supported values: prometheus, checkpoint (default)`)
	// prometheus history provider configs
	historyLength       = flag.String("history-length", "8d", `How much time back prometheus have to be queried to get historical metrics`)
	podLabelPrefix      = flag.String("pod-label-prefix", "pod_label_", `Which prefix to look for pod labels in metrics`)
	podLabelsMetricName = flag.String("metric-for-pod-labels", "up{job=\"kubernetes-pods\"}", `Which metric to look for pod labels in metrics`)
	podNamespaceLabel   = flag.String("pod-namespace-label", "kubernetes_namespace", `Label name to look for container names`)
	podNameLabel        = flag.String("pod-name-label", "kubernetes_pod_name", `Label name to look for container names`)
	ctrNamespaceLabel   = flag.String("container-namespace-label", "namespace", `Label name to look for container names`)
	ctrPodNameLabel     = flag.String("container-pod-name-label", "pod_name", `Label name to look for container names`)
	ctrNameLabel        = flag.String("container-name-label", "name", `Label name to look for container names`)
)

func main() {
	// 初始化klog
	klog.InitFlags(nil)
	kube_flag.InitFlags()
	klog.V(1).Infof("Vertical Pod Autoscaler %s Recommender", common.VerticalPodAutoscalerVersion)


	config := createKubeConfig(float32(*kubeApiQps), int(*kubeApiBurst))

	// 创建HealthCheck对象并指定超时时间
	healthCheck := metrics.NewHealthCheck(*metricsFetcherInterval*5, true)
	metrics.Initialize(*address, healthCheck)
	metrics_recommender.Register()
	metrics_quality.Register()

	// 如果存储模式是prometheus，将useCheckpoints设为false，反之true
	useCheckpoints := *storage != "prometheus"
	// 创建一个新的recommender实例
	recommender := routines.NewRecommender(config, *checkpointsGCInterval, useCheckpoints)
	if useCheckpoints {
		// 如果存储模式为checkpoints，则调用InitFromCheckpoints将历史checkpoints加载到
		// clusterState中
		recommender.GetClusterStateFeeder().InitFromCheckpoints()
	} else {
		// prometheus的各项配置，如prometheus地址，查询历史长度等
		config := history.PrometheusHistoryProviderConfig{
			Address:                *prometheusAddress,
			HistoryLength:          *historyLength,
			PodLabelPrefix:         *podLabelPrefix,
			PodLabelsMetricName:    *podLabelsMetricName,
			PodNamespaceLabel:      *podNamespaceLabel,
			PodNameLabel:           *podNameLabel,
			CtrNamespaceLabel:      *ctrNamespaceLabel,
			CtrPodNameLabel:        *ctrPodNameLabel,
			CtrNameLabel:           *ctrNameLabel,
			CadvisorMetricsJobName: *prometheusJobName,
		}
		// 如果存储模式为prometheus，调用InitFromHistoryProvider来通过历史提供者初始化VPA
		recommender.GetClusterStateFeeder().InitFromHistoryProvider(history.NewPrometheusHistoryProvider(config))
	}

	ticker := time.Tick(*metricsFetcherInterval)
	// 每次经过指定间隔时间执行一次迭代
	for range ticker {
		// RunOnce实现recommender在一次迭代中所需的工作
		// 随后更新VPA对象中的推荐值
		recommender.RunOnce()
		// 将上次活动的时间更新为当前时间
		healthCheck.UpdateLastActivity()
	}

}

// 创建KubeConfig
func createKubeConfig(kubeApiQps float32, kubeApiBurst int) *rest.Config {
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("Failed to create config: %v", err)
	}
	config.QPS = kubeApiQps
	config.Burst = kubeApiBurst
	return config
}
