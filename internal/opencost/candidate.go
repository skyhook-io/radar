package opencost

import (
	"context"
	"errors"
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
	return kubecostTarget{k8s.GetClientInterface(), k8s.GetConfig(), k8s.GetContextName(), k8s.IsInCluster()}
}

func ProbeCandidate(ctx context.Context, config ManagerConfig, shared bool) error {
	probe, err := PrepareCandidate(config, shared)
	if err != nil {
		return err
	}
	if probe == nil {
		return nil
	}
	return probe(ctx)
}

// PrepareCandidate captures discovery and transport before releasing the cluster
// configuration lock. The returned probe does no live-cluster lookup.
func PrepareCandidate(config ManagerConfig, shared bool) (func(context.Context) error, error) {
	if config.Source == SourcePrometheus || config.Source == SourceAuto && !hasExplicitKubecostConfig(config) {
		return nil, nil
	}
	target := captureKubecostTarget()
	if err := validateKubecostConfigContext(config, target.contextName); err != nil {
		return nil, err
	}
	clusterID := config.ClusterID
	if shared && clusterID == "" {
		clusterID = "__radar_connection_check__"
	}
	if !shared {
		var err error
		clusterID, err = resolveKubecostClusterID(clusterID)
		if err != nil {
			return nil, err
		}
	}
	if config.URL != "" {
		return func(ctx context.Context) error {
			_, _, err := probeKubecostURL(ctx, config.URL, config.APIKey, clusterID)
			if shared && errors.Is(err, ErrKubecostNoData) {
				return nil
			}
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
