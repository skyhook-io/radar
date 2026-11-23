package opencost

import (
	"context"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
)

type kubecostTarget struct {
	forwards    *portforward.Manager
	contextName string
	inCluster   bool
}

func captureKubecostTarget() kubecostTarget {
	conn := k8s.Current()
	return kubecostTarget{conn.PortForwards(), conn.ContextName(), k8s.IsInCluster()}
}

// PrepareCandidate captures discovery and transport before releasing the cluster
// configuration lock. The returned probe uses only the captured client and
// config, so a context switch cannot redirect it.
func PrepareCandidate(config ManagerConfig) (func(context.Context) error, error) {
	if config.Source == SourcePrometheus || config.Source == SourceAuto && !hasExplicitKubecostConfig(config) {
		return nil, nil
	}
	target := captureKubecostTarget()
	if err := validateKubecostConfigContext(config, target.contextName); err != nil {
		return nil, err
	}
	clusterID, err := resolveKubecostClusterID(config.ClusterID)
	if err != nil {
		return nil, err
	}
	if config.URL != "" {
		return func(ctx context.Context) error {
			_, _, err := probeKubecostURL(ctx, config.URL, config.APIKey, clusterID)
			return err
		}, nil
	}
	aggregator, err := discoverKubecostAggregator()
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
