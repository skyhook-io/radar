package connectionruntime

import (
	"context"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/traffic"
)

type Set struct {
	Metrics *prometheus.Client
	ArgoCD  *argocd.Manager
	Traffic *traffic.Manager
	Cost    *opencost.Manager
}

func SetFor(_ *k8s.Connection) Set {
	return Set{
		Metrics: prometheus.GetClient(),
		ArgoCD:  argocd.CurrentManager(),
		Traffic: traffic.GetManager(),
		Cost:    opencost.CurrentManager(),
	}
}

func CaptureMetrics(ctx context.Context) (*prometheus.Client, error) {
	if err := connections.Refresh(config.IntegrationMetrics); err != nil {
		return nil, err
	}
	client := SetFor(k8s.BoundOrCurrent(ctx)).Metrics
	if client == nil {
		return nil, prometheus.ErrPrometheusUnavailable
	}
	return client, nil
}
