VPA的核心组件包括Admission Controller，Recommender，Updater

控制器AC会拦截Pod的创建请求，如果Pod与未设置为off模式的VPA配置匹配，控制器通过将推荐资源应用到Pod spec来重写请求。AC通过从Recommender获取推荐资源，如果调用超时或失败，控制器将采用缓存在VPA对象中的资源建议。如果这也是不可用的，控制器采取最初指定的资源。

Recommender是VPA的主要组件，它负责计算推荐资源。在启动时，Recommender从History Storage中获取所有Pod的历史资源利用率（无论他们是否使用VPA）以及Pod OOM事件的历史。Recommender把这些数据聚合起来并保存在内存中。  
在正常运行期间，recommender 通过Metrics Server的Metrics API实时更新资源利用率和新事件。此外，它还监控集群中所有Pod和VPA对象。对于与某个VPA selector匹配的Pod，recommender会计算推荐的资源并在VPA对
象上设置推荐值。  
一个VPA对象有一个资源建议，用户需要使用一个VPA来控制具有
相似资源使用模式的Pods。  
Recommender充当一个extension-apiserver，向外暴露一个同步方法，该方法需要Pod Spec和Pod metadata并返回推荐资源。  

VPA Updater负责将推荐的资源应用到现有的Pod中。它监控集群中所有的VPA对象和Pods，通过调用Recommender API来定期的获取对Pods的资源推荐。当推荐的资源配额与实际配置的资源明显不同时，Updater可能会更新这个Pod，这意味着要删除Pod并用推荐的资源重建这些Pods。

# Kubernetes Autoscaler

[![Build Status](https://travis-ci.org/kubernetes/autoscaler.svg?branch=master)](https://travis-ci.org/kubernetes/autoscaler) [![GoDoc Widget]][GoDoc]

This repository contains autoscaling-related components for Kubernetes.

## What's inside

[Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) - a component that automatically adjusts the size of a Kubernetes
Cluster so that all pods have a place to run and there are no unneeded nodes. Works with GCP, AWS and Azure. Version 1.0 (GA) was released with kubernetes 1.8.

[Vertical Pod Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler) - a set of components that automatically adjust the
amount of CPU and memory requested by pods running in the Kubernetes Cluster. Current state - beta.

[Addon Resizer](https://github.com/kubernetes/autoscaler/tree/master/addon-resizer) - a simplified version of vertical pod autoscaler that modifies
resource requests of a deployment based on the number of nodes in the Kubernetes Cluster. Current state - beta.

## Contact Info

Interested in autoscaling? Want to talk? Have questions, concerns or great ideas?

Please join us on #sig-autoscaling at https://kubernetes.slack.com/, or join one
of our weekly meetings.  See [the Kubernetes Community Repo](https://github.com/kubernetes/community/blob/master/sig-autoscaling/README.md) for more information.

## Getting the Code

Fork the repository in the cloud:
1. Visit https://github.com/kubernetes/autoscaler
1. Click Fork button (top right) to establish a cloud-based fork.

The code must be checked out as a subdirectory of `k8s.io`, and not `github.com`.

```shell
mkdir -p $GOPATH/src/k8s.io
cd $GOPATH/src/k8s.io
# Replace "$YOUR_GITHUB_USERNAME" below with your github username
git clone https://github.com/$YOUR_GITHUB_USERNAME/autoscaler.git
cd autoscaler
```

Please refer to Kubernetes [Github workflow guide] for more details.

[GoDoc]: https://godoc.org/k8s.io/autoscaler
[GoDoc Widget]: https://godoc.org/k8s.io/autoscaler?status.svg
[Github workflow guide]: https://github.com/kubernetes/community/blob/master/contributors/guide/github-workflow.md
