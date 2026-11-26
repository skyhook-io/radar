package opencost

import (
	"context"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
)

type kubecostTarget struct {
	cache       *k8s.ResourceCache
	forwards    *portforward.Manager
	contextName string
	inCluster   bool
}

func captureKubecostTarget() kubecostTarget {
	return kubecostTargetFor(k8s.Current(), k8s.IsInCluster())
}

func kubecostTargetFor(conn *k8s.Connection, inCluster bool) kubecostTarget {
	return kubecostTarget{
		cache:       conn.ResourceCache(),
		forwards:    conn.PortForwards(),
		contextName: conn.ContextName(),
		inCluster:   inCluster,
	}
}

// PrepareCandidate captures discovery and transport before releasing the cluster
// configuration lock. The returned probe uses only the captured client and
// config, so a context switch cannot redirect it.
func PrepareCandidate(config ManagerConfig) (func(context.Context) error, error) {
	return PrepareCandidateFor(k8s.Current(), k8s.IsInCluster(), config)
}

func PrepareCandidateFor(conn *k8s.Connection, inCluster bool, config ManagerConfig) (func(context.Context) error, error) {
	if config.Source == SourcePrometheus || config.Source == SourceAuto && !hasExplicitKubecostConfig(config) {
		return nil, nil
	}
	target := kubecostTargetFor(conn, inCluster)
	if err := validateKubecostConfigContext(config, target.contextName); err != nil {
		return nil, err
	}
	clusterID, err := resolveKubecostClusterIDFrom(target.cache, config.ClusterID)
	if err != nil {
		return nil, err
	}
	if config.URL != "" {
		return func(ctx context.Context) error {
			_, _, err := probeKubecostURL(ctx, config.URL, config.APIKey, clusterID)
			return err
		}, nil
	}
	aggregator, err := discoverKubecostAggregatorFrom(target.cache)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		const owner = "cost-candidate"
		connection, err := connectDiscoveredKubecost(ctx, config, clusterID, aggregator, owner, target)
		connection.lease.stop()
		return err
	}, nil
}
