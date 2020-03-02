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

package main

import (
	"flag"
	"fmt"

	"net/http"
	"os"
	"time"

	"k8s.io/autoscaler/vertical-pod-autoscaler/common"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/logic"
	vpa_clientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/target"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/limitrange"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics"
	metrics_admission "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/admission"
	vpa_api_util "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
	"k8s.io/client-go/informers"
	kube_client "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	kube_flag "k8s.io/component-base/cli/flag"
	"k8s.io/klog"
)

const (
	defaultResyncPeriod time.Duration = 10 * time.Minute
)

var (
	certsConfiguration = &certsConfig{
		clientCaFile:  flag.String("client-ca-file", "/etc/tls-certs/caCert.pem", "Path to CA PEM file."),
		tlsCertFile:   flag.String("tls-cert-file", "/etc/tls-certs/serverCert.pem", "Path to server certificate PEM file."),
		tlsPrivateKey: flag.String("tls-private-key", "/etc/tls-certs/serverKey.pem", "Path to server certificate key PEM file."),
	}

	port           = flag.Int("port", 8000, "The port to listen on.")
	address        = flag.String("address", ":8944", "The address to expose Prometheus metrics.")
	namespace      = os.Getenv("NAMESPACE")
	webhookAddress = flag.String("webhook-address", "", "Address under which webhook is registered. Used when registerByURL is set to true.")
	webhookPort    = flag.String("webhook-port", "", "Server Port for Webhook")
	registerByURL  = flag.Bool("register-by-url", false, "If set to true, admission webhook will be registered by URL (webhookAddress:webhookPort) instead of by service name")
)
/**
certsConfiguration 部分是证书的config配置区域，将指定的CA证书或者server证书地址进行config。
port：8000是adminssion-controller对外暴露的端口，address是普罗米修斯对外暴露的metrics端口，用于监控数据变化。
webhookAddress:webhookPort提供了注册url，在这里替代server name来注册。
*/


func main() {
	//第一步：打印初始化参数
	klog.InitFlags(nil)
	kube_flag.InitFlags()
	klog.V(1).Infof("Vertical Pod Autoscaler %s Admission Controller", common.VerticalPodAutoscalerVersion)

	//第二步：创建metrics的连接，并且进行监控，对pods的数量和资源状态进行监控，具体过程可以在接口中看到。
	healthCheck := metrics.NewHealthCheck(time.Minute, false)
	metrics.Initialize(*address, healthCheck)
	// 注册普罗米修斯和healthCheck的服务
	metrics_admission.Register()
	// 注册对admission的pods数量和延迟的监控

	//第三步：初始化获取cert证书参数
	certs := initCerts(*certsConfiguration)

	//第四步：通过rest接口获取cluster参数
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatal(err)
	}

	//第五步：创建vpa客户端和lister（列出所有vpa），创建k8s客户端
	vpaClient := vpa_clientset.NewForConfigOrDie(config)
	vpaLister := vpa_api_util.NewAllVpasLister(vpaClient, make(chan struct{}))
	// 注意lister的信息全都放到了cache缓存中
	kubeClient := kube_client.NewForConfigOrDie(config)

	//第六步：informer
	//informer工厂，调用NewSharedInformerFactory()接口
	//疑问一：调用informers作用  答：创建一个名为 SharedInformerFactory 的单例工厂，因为每个Informer都会与Api Server维持一个watch长连接。
	factory := informers.NewSharedInformerFactory(kubeClient, defaultResyncPeriod)
	//sharedIndexInformer 是一个共享的 Informer 框架
	//vpa只需要提供一个模板类（比如 deploymentInformer ），便可以创建一个符合自己需求的特定 Informer。

	//第七步：target
	//疑问二：target是个什么？
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

	//第八步：Preprocessor
	//疑问三：两个Preprocessor什么意思？
	// 答：预先处理
	podPreprocessor := logic.NewDefaultPodPreProcessor()
	vpaPreprocessor := logic.NewDefaultVpaPreProcessor()

	//第九步：限制计算
	var limitRangeCalculator limitrange.LimitRangeCalculator
	//疑问四：通过factory来限制计算，原理？
	// 答： 通过factory的sharedIndexInformer工厂来实例化limitrange这个资源对象，通过该资源对象获取API Server中的资源计算限制信息
	limitRangeCalculator, err = limitrange.NewLimitsRangeCalculator(factory)
	if err != nil {
		klog.Errorf("Failed to create limitRangeCalculator, falling back to not checking limits. Error message: %s", err)
		limitRangeCalculator = limitrange.NewNoopLimitsCalculator()
	}

	//第十步：连接Recommendation（关键步骤）
	// 控制器AC会拦截Pod的创建请求，如果Pod与未设置为off模式的VPA配置匹配，控制器通过将推荐资源应用到Pod spec来重写请求。
	// AC通过从Recommender获取推荐资源，如果调用超时或失败，控制器将采用缓存在VPA对象中的资源建议。如果这也是不可用的，控制器采取最初指定的资源。
	recommendationProvider := logic.NewRecommendationProvider(limitRangeCalculator, vpa_api_util.NewCappingRecommendationProcessor(limitRangeCalculator), targetSelectorFetcher, vpaLister)

	//十一步：Admission Server（关键）
	// 创建Admission Server服务as
	as := logic.NewAdmissionServer(recommendationProvider, podPreprocessor, vpaPreprocessor, limitRangeCalculator)

	// handle函数来处理服务传递（http）
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		as.Serve(w, r)
		// 启动服务
		healthCheck.UpdateLastActivity()
		// 更新healthCheck，持续监控
	})

	clientset := getClient()
	// 获取k8s集群客户端
	server := &http.Server{
		Addr:      fmt.Sprintf(":%d", *port),
		TLSConfig: configTLS(clientset, certs.serverCert, certs.serverKey),
	}

	url := fmt.Sprintf("%v:%v", *webhookAddress, *webhookPort)
	//协程注册ca认证
	go selfRegistration(clientset, certs.caCert, &namespace, url, *registerByURL)
	//持续监听服务
	server.ListenAndServeTLS("", "")
}
