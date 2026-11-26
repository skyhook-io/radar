package connectionruntime

import (
	"context"
	"io"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/helm"
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
	Helm    *helm.UninstallMemo
}

func SetFor(conn *k8s.Connection) Set {
	return Set{
		Metrics: prometheus.GetClient(),
		ArgoCD:  argocd.CurrentManager(),
		Traffic: traffic.GetManager(),
		Cost:    opencost.CurrentManager(),
		Helm:    HelmMemoFor(conn),
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

func Build(_ context.Context, _ *k8s.Connection) (io.Closer, func()) {
	return &Set{Helm: helm.NewUninstallMemo()}, nil
}

func (s *Set) Close() error { return nil }

func HelmMemoFor(conn *k8s.Connection) *helm.UninstallMemo {
	if set, ok := conn.Integrations().(*Set); ok {
		return set.Helm
	}
	return nil
}
