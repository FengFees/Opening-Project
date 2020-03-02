/*
Copyright The Kubernetes Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package versioned

import (
	"fmt"

	autoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/typed/autoscaling.k8s.io/v1"
	autoscalingv1beta1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/typed/autoscaling.k8s.io/v1beta1"
	autoscalingv1beta2 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/typed/autoscaling.k8s.io/v1beta2"
	pocv1alpha1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/typed/poc.autoscaling.k8s.io/v1alpha1"
	discovery "k8s.io/client-go/discovery"
	rest "k8s.io/client-go/rest"
	flowcontrol "k8s.io/client-go/util/flowcontrol"
)

type Interface interface {
	Discovery() discovery.DiscoveryInterface
	AutoscalingV1() autoscalingv1.AutoscalingV1Interface
	AutoscalingV1beta2() autoscalingv1beta2.AutoscalingV1beta2Interface
	AutoscalingV1beta1() autoscalingv1beta1.AutoscalingV1beta1Interface
	PocV1alpha1() pocv1alpha1.PocV1alpha1Interface
}

// Clientset contains the clients for groups. Each group has exactly one
// version included in a Clientset.
type Clientset struct {
	*discovery.DiscoveryClient
	autoscalingV1      *autoscalingv1.AutoscalingV1Client
	autoscalingV1beta2 *autoscalingv1beta2.AutoscalingV1beta2Client
	autoscalingV1beta1 *autoscalingv1beta1.AutoscalingV1beta1Client
	pocV1alpha1        *pocv1alpha1.PocV1alpha1Client
}

// AutoscalingV1 retrieves the AutoscalingV1Client
func (c *Clientset) AutoscalingV1() autoscalingv1.AutoscalingV1Interface {
	return c.autoscalingV1
}

// AutoscalingV1beta2 retrieves the AutoscalingV1beta2Client
func (c *Clientset) AutoscalingV1beta2() autoscalingv1beta2.AutoscalingV1beta2Interface {
	return c.autoscalingV1beta2
}

// AutoscalingV1beta1 retrieves the AutoscalingV1beta1Client
func (c *Clientset) AutoscalingV1beta1() autoscalingv1beta1.AutoscalingV1beta1Interface {
	return c.autoscalingV1beta1
}

// PocV1alpha1 retrieves the PocV1alpha1Client
func (c *Clientset) PocV1alpha1() pocv1alpha1.PocV1alpha1Interface {
	return c.pocV1alpha1
}

// Discovery retrieves the DiscoveryClient
func (c *Clientset) Discovery() discovery.DiscoveryInterface {
	if c == nil {
		return nil
	}
	return c.DiscoveryClient
}

// NewForConfig creates a new Clientset for the given config.
// If config's RateLimiter is not set and QPS and Burst are acceptable,
// NewForConfig will generate a rate-limiter in configShallowCopy.
func NewForConfig(c *rest.Config) (*Clientset, error) {
	configShallowCopy := *c
	if configShallowCopy.RateLimiter == nil && configShallowCopy.QPS > 0 {
		if configShallowCopy.Burst <= 0 {
			return nil, fmt.Errorf("Burst is required to be greater than 0 when RateLimiter is not set and QPS is set to greater than 0")
		}
		configShallowCopy.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(configShallowCopy.QPS, configShallowCopy.Burst)
	}
	var cs Clientset
	var err error
	cs.autoscalingV1, err = autoscalingv1.NewForConfig(&configShallowCopy)
	if err != nil {
		return nil, err
	}
	cs.autoscalingV1beta2, err = autoscalingv1beta2.NewForConfig(&configShallowCopy)
	if err != nil {
		return nil, err
	}
	cs.autoscalingV1beta1, err = autoscalingv1beta1.NewForConfig(&configShallowCopy)
	if err != nil {
		return nil, err
	}
	cs.pocV1alpha1, err = pocv1alpha1.NewForConfig(&configShallowCopy)
	if err != nil {
		return nil, err
	}

	cs.DiscoveryClient, err = discovery.NewDiscoveryClientForConfig(&configShallowCopy)
	if err != nil {
		return nil, err
	}
	return &cs, nil
}

// NewForConfigOrDie creates a new Clientset for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *rest.Config) *Clientset {
	var cs Clientset
	cs.autoscalingV1 = autoscalingv1.NewForConfigOrDie(c)
	cs.autoscalingV1beta2 = autoscalingv1beta2.NewForConfigOrDie(c)
	cs.autoscalingV1beta1 = autoscalingv1beta1.NewForConfigOrDie(c)
	//获取各个版本的配置信息
	cs.pocV1alpha1 = pocv1alpha1.NewForConfigOrDie(c)

	cs.DiscoveryClient = discovery.NewDiscoveryClientForConfigOrDie(c)
	//获取客户端信息
	return &cs
}

// New creates a new Clientset for the given RESTClient.
func New(c rest.Interface) *Clientset {
	var cs Clientset
	cs.autoscalingV1 = autoscalingv1.New(c)
	cs.autoscalingV1beta2 = autoscalingv1beta2.New(c)
	cs.autoscalingV1beta1 = autoscalingv1beta1.New(c)
	cs.pocV1alpha1 = pocv1alpha1.New(c)

	cs.DiscoveryClient = discovery.NewDiscoveryClient(c)
	return &cs
}
