package connectionruntime

import (
	"context"
	"errors"
	"reflect"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/openobserve"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/pkg/argoapi"
	"github.com/skyhook-io/radar/pkg/prom"
)

type Set struct {
	Metrics          *prometheus.Client
	ArgoCD           *argocd.Manager
	Traffic          *traffic.Manager
	Cost             *opencost.Manager
	Currency         *opencost.CurrencyResolver
	Helm             *helm.UninstallMemo
	settings         Settings
	observe          func()
	unavailableScans *prometheus.RightsizingScanManager
}

type Settings struct {
	observeTarget                      openobserve.Target
	observeConfig                      *config.OpenObserveConfig
	scopeCleared                       bool
	Metrics                            prom.Connection
	MetricsError                       error
	ArgoCD                             argoapi.Connection
	ArgoError                          error
	ArgoEnv                            bool
	ArgoTokenContext, ArgoTokenBinding string
	Cost                               opencost.ManagerConfig
	CostError                          error
	CostEnv                            bool
	Currency                           string
	Scope                              prom.WorkloadMetricsScope
	BeylaSelector                      string
	relay                              *prometheus.RelayConfig
}

func SetFor(conn *k8s.Connection) Set {
	if s := ownedSetFor(conn); s != nil {
		return *s
	}
	return Set{}
}

func ownedSetFor(conn *k8s.Connection) *Set {
	s, _ := conn.Integrations().(*Set)
	return s
}

func (s Set) ArgoSettingsError() error { return s.settings.ArgoError }

func Capture(ctx context.Context) Set { return SetFor(k8s.BoundOrCurrent(ctx)) }

func CaptureMetrics(ctx context.Context) (*prometheus.Client, error) {
	return Capture(ctx).MetricsClient()
}

func (s Set) MetricsClient() (*prometheus.Client, error) {
	if err := s.settings.metricsError(); err != nil {
		return nil, err
	}
	if s.Metrics == nil {
		return nil, prometheus.ErrPrometheusUnavailable
	}
	return s.Metrics, nil
}

func (s Set) MetricsAvailability(ctx context.Context) prometheus.AvailabilityState {
	if client, err := s.MetricsClient(); err == nil {
		return client.Availability(ctx)
	} else if errors.Is(err, prometheus.ErrPrometheusUnavailable) {
		return prometheus.AvailabilityState{State: prometheus.AvailabilityAbsent}
	} else {
		return prometheus.AvailabilityState{State: prometheus.AvailabilityConfiguredFailed, Err: err}
	}
}

func (s Set) RightsizingScans() *prometheus.RightsizingScanManager {
	if s.Metrics != nil {
		return s.Metrics.RightsizingScans()
	}
	return s.unavailableScans
}

func build(conn *k8s.Connection, settings Settings, previous *Set) *Set {
	if previous == nil {
		previous = &Set{Helm: helm.NewUninstallMemo()}
	}
	s := *previous
	s.settings = settings
	old := previous.settings
	inCluster := k8s.IsInCluster()
	newMetrics := previous.Traffic == nil || !reflect.DeepEqual(settings.Metrics, old.Metrics) ||
		errorText(settings.metricsError()) != errorText(old.metricsError()) || !sameRelayBackend(settings.relay, old.relay) ||
		!reflect.DeepEqual(settings.Scope, old.Scope) || settings.scopeCleared != old.scopeCleared || !reflect.DeepEqual(settings.observeConfig, old.observeConfig)
	if newMetrics {
		s.Metrics, s.observe, s.unavailableScans = nil, nil, nil
		if err := settings.metricsError(); err != nil {
			s.unavailableScans = prometheus.NewUnavailableRightsizingScans(err)
		} else {
			s.Metrics = prometheus.NewClient(conn.ClientInterface(), conn.Config(), conn.ContextName(), conn.PortForwards(), inCluster, conn.ResourceCache())
			s.Metrics.Configure(settings.Metrics.URL, settings.Metrics.Headers)
			if settings.scopeCleared {
				s.Metrics.NoteWorkloadScopeCleared()
			}
			if settings.Scope.SingleCluster || len(settings.Scope.ClusterLabels) != 0 {
				_ = s.Metrics.SetWorkloadMetricsScope(settings.Scope, settings.BeylaSelector)
			}
			if settings.relay != nil {
				_ = s.Metrics.EnableRelay(*settings.relay)
			} else if settings.observeConfig != nil && settings.observeTarget.Client != nil {
				activator := openobserve.NewConnectionActivator(s.Metrics, s.Metrics.Lifetime())
				s.observe = func() { activator.ApplyConfig(settings.observeTarget, settings.observeConfig) }
			}
		}
		s.Traffic = traffic.NewConnectionManager(conn, s.Metrics, settings.BeylaSelector)
	}
	if previous.ArgoCD == nil || settings.ArgoCD != old.ArgoCD ||
		settings.ArgoTokenBinding != old.ArgoTokenBinding || settings.ArgoTokenContext != old.ArgoTokenContext ||
		errorText(settings.ArgoError) != errorText(old.ArgoError) || settings.ArgoEnv != old.ArgoEnv {
		s.ArgoCD = argocd.NewConnectionManager(conn, inCluster, conn.ContextBinding())
		switch {
		case settings.ArgoError != nil:
			s.ArgoCD.SeedFromEnvFailed(settings.ArgoError.Error())
		case settings.ArgoEnv:
			s.ArgoCD.SeedFromEnv(settings.ArgoCD.URL, settings.ArgoCD.Token, settings.ArgoCD.InsecureTLS)
		default:
			s.ArgoCD.RestoreConfig(settings.ArgoCD.URL, settings.ArgoCD.Token, settings.ArgoCD.InsecureTLS, settings.ArgoTokenContext, settings.ArgoTokenBinding)
		}
	}
	if newMetrics || previous.Cost == nil || settings.Cost != old.Cost ||
		errorText(settings.CostError) != errorText(old.CostError) || settings.CostEnv != old.CostEnv {
		s.Cost = opencost.NewManager(conn, inCluster, s.Metrics, settings.metricsError())
		if err := s.Cost.ConfigureResolved(settings.Cost, settings.CostEnv, settings.CostError); err != nil {
			_ = s.Cost.ConfigureResolved(opencost.ManagerConfig{}, settings.CostEnv, err)
		}
	}
	if s.Cost != previous.Cost || settings.Currency != old.Currency {
		s.Currency = opencost.NewConnectionCurrencyResolver(settings.Currency, conn, s.Cost, s.Metrics)
	}
	return &s
}

func (s Settings) metricsError() error {
	if s.relay == nil {
		return s.MetricsError
	}
	return nil
}

func sameRelayBackend(a, b *prometheus.RelayConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Key == b.Key
}

func (s *Set) start(previous *Set) {
	if previous == nil || previous.Metrics != s.Metrics {
		s.Metrics.Prewarm()
		if s.observe != nil {
			go s.observe()
		}
	}
	if s.ArgoCD != nil && (previous == nil || previous.ArgoCD != s.ArgoCD) {
		s.ArgoCD.Prewarm()
	}
}

func (s *Set) closeDisplaced(kept *Set) {
	if s == nil {
		return
	}
	if kept == nil {
		kept = &Set{}
	}
	if s.Metrics != kept.Metrics {
		_ = s.Metrics.Close()
	}
	if s.unavailableScans != nil && s.unavailableScans != kept.unavailableScans {
		s.unavailableScans.Invalidate()
	}
	if s.ArgoCD != kept.ArgoCD {
		_ = s.ArgoCD.Close()
	}
	if s.Cost != kept.Cost {
		_ = s.Cost.Close()
	}
	if s.Traffic != nil && s.Traffic != kept.Traffic {
		_ = s.Traffic.Close()
	}
}

func (s *Set) Close() error {
	s.closeDisplaced(nil)
	return nil
}

func HelmMemoFor(conn *k8s.Connection) *helm.UninstallMemo {
	if set, ok := conn.Integrations().(*Set); ok {
		return set.Helm
	}
	return nil
}
