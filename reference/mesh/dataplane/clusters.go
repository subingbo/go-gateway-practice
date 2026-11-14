package dataplane

import (
	"example.com/go-gateway-reference/internal/meshapi"
	"example.com/go-gateway-reference/internal/upstream"
)

type clusterSpec struct {
	host      string
	subset    string
	policy    meshapi.TrafficPolicy
	endpoints []upstream.EndpointInfo
}

// Sync 用最新快照重建集群与实例列表。
//
// 每个服务都会有一个默认集群（subset 为空），DestinationRule 里定义
// 的每个 subset 也会有一个独立集群，便于按版本分流和独立熔断。
func (r *Router) Sync(snapshot *meshapi.Snapshot) {
	if snapshot == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if snapshot.TrustBundlePEM != "" && snapshot.TrustBundlePEM != r.trustBundle {
		r.trustBundle = snapshot.TrustBundlePEM

		if err := r.roots.SetPEM(snapshot.TrustBundlePEM); err != nil {
			r.logger.Error("update trust bundle failed", "error", err)
		}

		for _, entry := range r.transports {
			entry.transport.CloseIdleConnections()
		}
		r.transports = make(map[string]*transportEntry)
	}

	desired := make(map[string]clusterSpec)

	for _, service := range snapshot.Services {
		dr := snapshot.FindDestinationRule(service.Name)

		policy := meshapi.TrafficPolicy{}
		if dr != nil {
			policy = dr.Spec.TrafficPolicy
		}

		desired[clusterKey(service.Name, "")] = clusterSpec{
			host:      service.Name,
			policy:    policy,
			endpoints: filterInstances(service.Instances, nil),
		}

		if dr == nil {
			continue
		}

		for _, subset := range dr.Spec.Subsets {
			desired[clusterKey(service.Name, subset.Name)] = clusterSpec{
				host:      service.Name,
				subset:    subset.Name,
				policy:    policy,
				endpoints: filterInstances(service.Instances, subset.Labels),
			}
		}
	}

	for key, spec := range desired {
		entry, ok := r.clusters[key]
		if !ok || entry.policy != spec.policy {
			entry = r.newClusterLocked(spec)
			r.clusters[key] = entry
		}

		entry.cluster.SetEndpoints(spec.endpoints)
	}

	for key := range r.clusters {
		if _, ok := desired[key]; !ok {
			delete(r.clusters, key)
		}
	}
}

func (r *Router) clusterFor(host, subset string) *clusterEntry {
	key := clusterKey(host, subset)

	r.mu.Lock()
	entry := r.clusters[key]
	r.mu.Unlock()

	if entry != nil {
		return entry
	}

	// 快照可能刚更新，而 Sync 回调还没执行；这里按需补一次。
	snapshot := r.snapshot()
	if snapshot == nil {
		return nil
	}

	r.Sync(snapshot)

	r.mu.Lock()
	entry = r.clusters[key]
	r.mu.Unlock()

	return entry
}

func (r *Router) newClusterLocked(spec clusterSpec) *clusterEntry {
	name := spec.host
	if spec.subset != "" {
		name += "/" + spec.subset
	}

	cluster := upstream.NewCluster(
		name,
		spec.policy.LoadBalancer,
		upstream.OutlierPolicy{
			Consecutive5xx: spec.policy.OutlierDetection.Consecutive5xx,
			EjectionTime:   spec.policy.OutlierDetection.EjectionTime,
		},
		spec.policy.ConnectionPool.MaxPendingRequests,
	)

	cluster.SetOnEject(func(endpoint string) {
		r.logger.Warn("endpoint ejected",
			"destination", spec.host,
			"subset", spec.subset,
			"endpoint", endpoint,
		)
		r.metrics.incEjection(r.source, spec.host, endpoint)
	})

	return &clusterEntry{
		cluster: cluster,
		host:    spec.host,
		subset:  spec.subset,
		policy:  spec.policy,
	}
}

func filterInstances(instances []meshapi.Instance, labels map[string]string) []upstream.EndpointInfo {
	out := make([]upstream.EndpointInfo, 0, len(instances))

	for _, instance := range instances {
		if !instance.Status.Healthy() {
			continue
		}
		if !labelsMatch(instance.Labels, labels) {
			continue
		}

		out = append(out, upstream.EndpointInfo{
			Addr:    instance.Address,
			Labels:  instance.Labels,
			Healthy: true,
		})
	}

	return out
}

func labelsMatch(have, want map[string]string) bool {
	for key, value := range want {
		if have[key] != value {
			return false
		}
	}
	return true
}

func clusterKey(host, subset string) string {
	return host + "\x00" + subset
}
