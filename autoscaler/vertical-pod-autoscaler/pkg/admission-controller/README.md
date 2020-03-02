# VPA Admission Controller

- [Intro](#intro)
- [Running](#running)
- [Implementation](#implmentation)

## Intro

This is a binary that registers itself as a Mutating Admission Webhook
and because of that is on the path of creating all pods.
For each pod creation, it will get a request from the apiserver and it will
either decide there's no matching VPA configuration or find the corresponding
one and use current recommendation to set resource requests in the pod.

控制器AC会拦截Pod的创建请求，如果Pod与未设置为off模式的VPA配置匹配，控制器通过将推荐资源应用到Pod spec来重写请求。AC通过从Recommender获取推荐资源，如果调用超时或失败，控制器将采用缓存在VPA对象中的资源建议。如果这也是不可用的，控制器采取最初指定的资源

## Running
运行要求：
要求API Server支持可修改的Webhooks（回调），参数符合，修改完参数后重启kubelet。创建secret，RBAC认证
，pod，并进行注册
1. You should make sure your API server supports Mutating Webhooks.
Its `--admission-control` flag should have `MutatingAdmissionWebhook` as one of
the values on the list and its `--runtime-config` flag should include
`admissionregistration.k8s.io/v1beta1=true`.
To change those flags, ssh to your master instance, edit
`/etc/kubernetes/manifests/kube-apiserver.manifest` and restart kubelet to pick
up the changes: ```sudo systemctl restart kubelet.service```
1. Generate certs by running `bash gencerts.sh`. This will use kubectl to create
   a secret in your cluster with the certs.
1. Create RBAC configuration for the admission controller pod by running
   `kubectl create -f ../deploy/admission-controller-rbac.yaml`
1. Create the pod:
   `kubectl create -f ../deploy/admission-controller-deployment.yaml`.
   The first thing this will do is it will register itself with the apiserver as
   Webhook Admission Controller and start changing resource requirements
   for pods on their creation & updates.
1. You can specify a path for it to register as a part of the installation process
   by setting `--register-by-url=true` and passing `--webhook-address` and `--webhook-port`.

## Implementation
集群中的所有VPA配置都通过lister进行监视。
在创建pod的上下文中，有一个来自apiserver的传入https请求。
服务该请求的逻辑包括找到适当的VPA，从中检索当前recommendation，
并将recommendation编码为json补丁到Pod资源。
All VPA configurations in the cluster are watched with a lister.
In the context of pod creation, there is an incoming https request from
apiserver.
The logic to serve that request involves finding the appropriate VPA, retrieving
current recommendation from it and encodes the recommendation as a json patch to
the Pod resource.

控制器ac部分的源码阅读已经放置在该文件夹下面的Admission-Controller.pdf中