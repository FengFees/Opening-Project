简介: VPA Recommender是VPA系统的核心组件。它基于历史和目前的资源使用情况来计算出一个推荐的资源需求。这个推荐值被放在可以查看的VPA资源状态里。

运行: 
* 为了让recommender能获取历史数据，应该在集群上安装Prometheus
并且把它的地址通过一个标志来传递。
* 通过../deploy/vpa-rbac.yaml来创建RBAC配置。
* 从../deploy/recommender-deployment.yaml的recommender pod来创建
一个deployment。
* recommender会开始运行并把推荐值推送到VPA对象状态里。

实现: recommender基于它在内存中建立的模型工作。这个模型包含kubernetes
资源，包括pod, vpa,它们的配置和一些其他的信息(每个容器的使用数据)
在启动后，recommender会将在运行pod的历史数据通过prometheus
读进模型。
接着recommender会运行一个循环，循环的每一轮步骤如下：
1. 根据资源最近的信息更新模型
2. 使用从Metrics API得到的新使用情况样本更新模型
3. 为每个VPA计算新的推荐值
4. 把更新的推荐值放到VPA资源里。




# VPA Recommender

- [Intro](#intro)
- [Running](#running)
- [Implementation](#implmentation)
## Intro

Recommender is the core binary of Vertical Pod Autoscaler system.
It computes the recommended resource requests for pods based on
historical and current usage of the resources.
The current recommendations are put in status of the VPA resource, where they
can be inspected.

## Running

* In order to have historical data pulled in by the recommender, install
  Prometheus in your cluster and pass its address through a flag.
* Create RBAC configuration from `../deploy/vpa-rbac.yaml`.
* Create a deployment with the recommender pod from
  `../deploy/recommender-deployment.yaml`.
* The recommender will start running and pushing its recommendations to VPA
  object statuses.

## Implementation

The recommender is based on a model of the cluster that it builds in its memory.
The model contains Kubernetes resources: *Pods*, *VerticalPodAutoscalers*, with
their configuration (e.g. labels) as well as other information, e.g. usage data for
each container.

After starting the binary, recommender reads the history of running pods and
their usage from Prometheus into the model.
It then runs in a loop and at each step performs the following actions:

* update model with recent information on resources (using listers based on
  watch),
* update model with fresh usage samples from Metrics API,
* compute new recommendation for each VPA,
* put any changed recommendations into the VPA resources.

