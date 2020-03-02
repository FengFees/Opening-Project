# Vertical Pod Autoscaler - Updater

- [Introduction](#introduction)
- [Current implementation](current-implementation)
- [Missing parts](#missing-parts)

# Introduction
Updater component for Vertical Pod Autoscaler described in https://github.com/kubernetes/community/pull/338

Updater runs in Kubernetes cluster and decides which pods should be restarted
based on resources allocation recommendation calculated by Recommender.
If a pod should be updated, Updater will try to evict the pod.
It respects the pod disruption budget, by using Eviction API to evict pods.
Updater does not perform the actual resources update, but relies on Vertical Pod Autoscaler admission plugin
to update pod resources when the pod is recreated after eviction.

Updater用于k8s集群，决定哪些pod根据Recommender计算的资源分配策略需要被重新创建。
如果一个pod必须要被更新，Updater将尝试驱逐Pod，遵循pod的破坏规则，通过使用Eviction API驱逐pods。
Updater不会执行实际的资源更新，而是依靠Vertical Pod Autoscaler的admission插件，更新被驱逐后重建pod的资源。

# Current implementation
Runs in a loop. On one iteration performs:
* Fetching Vertical Pod Autoscaler configuration using a lister implementation.
* Fetching live pods information with their current resource allocation.
* For each replicated pods group calculating if pod update is required and how many replicas can be evicted.
Updater will always allow eviction of at least one pod in replica set. Maximum ratio of evicted replicas is specified by flag.
* Evicting pods if recommended resources significantly vary from the actual resources allocation.
Threshold for evicting pods is specified by recommended min/max values from VPA resource.
Priority of evictions within a set of replicated pods is proportional to sum of percentages of changes in resources
(i.e. pod with 15% memory increase 15% cpu decrease recommended will be evicted
before pod with 20% memory increase and no change in cpu).

按照时间戳进行主循环，主循环中存在迭代，其中的每一次迭代执行：
*使用lister implementation实现获取Vertical Pod Autoscaler配置。
*获取存在的pods信息及它们当前资源分配情况。
*对于每个replicated Pod组，计算是否需要Pod更新以及可以驱逐多少个replicas。
Updater将始终允许逐出replica set中的至少一个Pod。 逐出副本的最大比例由flag指定。
*如果推荐资源与实际资源分配有很大差异，则驱逐pods。
驱逐pods的阈值由VPA资源中最小/最大值的recommendation指定。
一组复制吊舱内的逐出优先级与资源变化百分比之和成正比
（即建议内存增加15％的pod会在内存增加到20％且cpu不变的情况下淘汰CPU增加15％的pod）。

# Missing parts
* Recommendation API for fetching data from Vertical Pod Autoscaler Recommender.
* Monitoring.
