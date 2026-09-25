package connectionruntime

import (
	"errors"
	"log"
	"maps"
	"reflect"
	"strings"
	"sync"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/traffic"
)

type Runtime struct {
	Resolver       *connections.Resolver
	mu             sync.Mutex
	target         k8s.ProfileTarget
	active         map[config.Integration]connections.Selection
	restartTraffic func() error
}

func New(resolver *connections.Resolver, restartTraffic func() error) *Runtime {
	return &Runtime{Resolver: resolver, active: map[config.Integration]connections.Selection{}, restartTraffic: restartTraffic}
}

func (r *Runtime) Refresh(kind config.Integration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k8s.GetConnectionStatus().State != k8s.StateConnected {
		return errors.New("connect to Kubernetes before using this context's integrations")
	}
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		return err
	}
	selected := r.resolve(target, false)
	changed := false
	for _, integration := range config.IntegrationKinds {
		changed = changed || r.changed(target, integration, selected[integration])
	}
	if changed {
		err = k8s.TryClusterConfiguration(func() error {
			current, err := k8s.CurrentProfileTarget()
			if err != nil || !current.Same(target) {
				return k8s.ErrContextConfigurationBusy
			}
			r.activate(target, selected, false)
			return nil
		})
		if err != nil {
			return err
		}
	}
	current, err := k8s.CurrentProfileTarget()
	if err != nil || !current.Same(target) {
		return k8s.ErrContextConfigurationBusy
	}
	if s := selected[kind]; s.Err != nil {
		return &connections.SettingsError{Kind: kind, Launch: s.View.State == "launch", Err: s.Err}
	}
	return nil
}

// Callers hold the cluster configuration lock or run before the cluster is
// initialized. Apply never probes a remote integration.
func (r *Runtime) Apply(target k8s.ProfileTarget, details bool) map[config.Integration]connections.Selection {
	r.mu.Lock()
	defer r.mu.Unlock()
	selected := r.resolve(target, details)
	if k8s.GetConnectionStatus().State == k8s.StateConnected {
		r.activate(target, selected, false)
	}
	return selected
}

// ActivateSwitch runs after the switch has rebuilt clients, before it publishes
// Connected. It skips the traffic restart because the switch already
// reinitialized traffic.
func (r *Runtime) ActivateSwitch(target k8s.ProfileTarget) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activate(target, r.resolve(target, false), true)
}

func (r *Runtime) resolve(target k8s.ProfileTarget, details bool) map[config.Integration]connections.Selection {
	return r.Resolver.ResolveAll(target, details)
}

func (r *Runtime) changed(target k8s.ProfileTarget, kind config.Integration, s connections.Selection) bool {
	old, exists := r.active[kind]
	return !exists || r.target.Binding != target.Binding || r.target.Fingerprint != target.Fingerprint || !reflect.DeepEqual(old.Settings, s.Settings) || errorText(old.Err) != errorText(s.Err)
}

func (r *Runtime) activate(target k8s.ProfileTarget, selected map[config.Integration]connections.Selection, switching bool) {
	metricsChanged := false
	for _, kind := range config.IntegrationKinds {
		s := selected[kind]
		if !r.changed(target, kind, s) {
			continue
		}
		switch kind {
		case config.IntegrationMetrics:
			if s.Err != nil {
				metricsChanged = prometheus.GetClient() != nil
				prometheus.Retire()
				traffic.SetMetricsConfig("", nil)
			} else {
				if prometheus.GetClient() == nil {
					prometheus.Initialize(k8s.GetClientInterface(), k8s.GetConfig(), k8s.GetContextName())
				}
				url, headers := prometheus.CurrentConfig()
				metricsChanged = url != strings.TrimRight(s.Settings.Prometheus.URL, "/") || !maps.Equal(headers, s.Settings.Prometheus.Headers)
				if metricsChanged {
					prometheus.Configure(s.Settings.Prometheus.URL, s.Settings.Prometheus.Headers)
				}
				traffic.SetMetricsConfig(s.Settings.Prometheus.URL, s.Settings.Prometheus.Headers)
			}
			opencost.Reset()
			opencost.InvalidateCurrency()
		case config.IntegrationArgoCD:
			if s.Err != nil {
				argocd.SeedFromEnvFailed(s.Err.Error())
			} else {
				c := *s.Settings.ArgoCD
				argocd.SetConfig(c.URL, c.Token, c.InsecureTLS, true)
			}
		case config.IntegrationCost:
			if s.Err != nil {
				opencost.DisableLocal(s.Err.Error())
			} else if err := opencost.Configure(CostConfig(s, target)); err != nil {
				opencost.DisableLocal(err.Error())
			}
			opencost.InvalidateCurrency()
		}
		r.active[kind] = s
	}
	r.target = target
	if metricsChanged && !switching && r.restartTraffic != nil {
		go func() {
			if err := r.restartTraffic(); err != nil {
				log.Printf("[connections] Refreshing traffic: %v", err)
			}
		}()
	}
}

func CostConfig(s connections.Selection, target k8s.ProfileTarget) opencost.ManagerConfig {
	c := s.Settings.Kubecost
	if c == nil {
		return opencost.ManagerConfig{Source: opencost.Source(s.Settings.EffectiveMode(config.IntegrationCost))}
	}
	return opencost.ManagerConfig{Source: opencost.Source(s.Settings.EffectiveMode(config.IntegrationCost)), URL: c.URL, APIKey: c.APIKey, APIKeyContext: target.Context, ClusterID: s.Settings.ClusterID, ClusterIDContext: target.Context}
}

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func (r *Runtime) CheckCurrent(target k8s.ProfileTarget, selection connections.Selection) error {
	current, err := k8s.CurrentProfileTarget()
	if err != nil || !current.Same(target) || !r.Resolver.IsCurrent(selection) {
		return errors.New("connection changed during the operation; reload Settings")
	}
	return nil
}
