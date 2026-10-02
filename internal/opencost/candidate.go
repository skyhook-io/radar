package opencost

import (
	"context"
	"sync"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var candidateMu sync.Mutex

type kubecostTarget struct {
	client      kubernetes.Interface
	config      *rest.Config
	contextName string
	inCluster   bool
}

func captureKubecostTarget() kubecostTarget {
	client, config, contextName, inCluster := k8s.GetClusterClientSnapshot()
	return kubecostTarget{client, config, contextName, inCluster}
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
		candidateMu.Lock()
		defer candidateMu.Unlock()
		const owner = "cost-candidate"
		defer portforward.Stop(owner)
		_, err := connectDiscoveredKubecost(ctx, config, clusterID, aggregator, owner, target)
		return err
	}, nil
}
