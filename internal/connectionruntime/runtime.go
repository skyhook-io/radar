package connectionruntime

import (
	"context"
	"io"
	"reflect"
	"sync/atomic"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/openobserve"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

type Runtime struct {
	Resolver          *connections.Resolver
	settings          atomic.Pointer[Settings]
	scopeCleared      atomic.Bool
	scopeConfigured   bool
	launchBinding     string
	launchFingerprint string
}

func New(resolver *connections.Resolver, startup Settings) *Runtime {
	r := &Runtime{Resolver: resolver, launchBinding: k8s.Current().ContextBinding(), scopeConfigured: startup.Scope.SingleCluster || len(startup.Scope.ClusterLabels) != 0}
	if r.scopeConfigured {
		target, _ := k8s.CurrentProfileTarget()
		r.launchFingerprint = target.Fingerprint
	}
	r.settings.Store(&startup)
	return r
}

func (r *Runtime) Snapshot() *Settings { return r.settings.Load() }

func (r *Runtime) Settings() Settings {
	if r == nil {
		return Settings{}
	}
	settings := *r.settings.Load()
	cleared := r.scopeConfigured && r.scopeCleared.Load()
	if cleared {
		settings.Scope = prom.WorkloadMetricsScope{}
	}
	settings.scopeCleared = cleared
	return settings
}

func (r *Runtime) ClearLaunchScope() { r.scopeCleared.Store(true) }

func (r *Runtime) Build(_ context.Context, conn *k8s.Connection) (io.Closer, func()) {
	if conn.ContextBinding() != r.launchBinding {
		r.ClearLaunchScope()
	}
	if r.Resolver == nil && r.launchFingerprint != "" && !r.scopeCleared.Load() {
		if target, err := k8s.ProfileTargetFor(conn); err != nil || target.Fingerprint != r.launchFingerprint {
			r.ClearLaunchScope()
		}
	}
	settings := r.Settings()
	if r.Resolver != nil {
		target, err := k8s.ProfileTargetFor(conn)
		if err != nil {
			settings.MetricsError, settings.ArgoError, settings.CostError = err, err, err
		} else {
			settings = r.profiles(conn, target, r.Resolver.ResolveAll(target, false))
		}
	}
	if settings.scopeCleared {
		r.ClearLaunchScope()
	}
	s := build(conn, settings, nil, nil)
	return s, func() { s.start(nil) }
}

func (r *Runtime) profiles(conn *k8s.Connection, target k8s.ProfileTarget, selected map[config.Integration]connections.Selection) Settings {
	settings := r.Settings()
	settings.observeConfig = config.Load().OpenObserve
	settings.observeTarget = openobserve.Target{Context: target.Context, Fingerprint: target.Fingerprint, Client: conn.ClientInterface()}
	metrics, argo, cost := selected[config.IntegrationMetrics], selected[config.IntegrationArgoCD], selected[config.IntegrationCost]
	if metrics.Settings.Prometheus != nil {
		settings.Metrics = *metrics.Settings.Prometheus
	}
	settings.MetricsError, settings.ArgoError, settings.CostError = settingsError(config.IntegrationMetrics, metrics), settingsError(config.IntegrationArgoCD, argo), settingsError(config.IntegrationCost, cost)
	settings.ArgoCD = *argo.Settings.ArgoCD
	settings.ArgoTokenContext, settings.ArgoTokenBinding = target.Context, target.Binding
	settings.Cost = CostConfig(cost, target)
	if target.Fingerprint != r.launchFingerprint || !reflect.DeepEqual(settings.Metrics, r.settings.Load().Metrics) || settings.MetricsError != nil {
		settings.Scope = prom.WorkloadMetricsScope{}
		settings.scopeCleared = r.scopeConfigured
	}
	return settings
}

func settingsError(kind config.Integration, selected connections.Selection) error {
	if selected.Err == nil {
		return nil
	}
	return &connections.SettingsError{Kind: kind, Launch: selected.View.State == "launch", Err: selected.Err}
}

type Edit struct {
	runtime       *Runtime
	conn          *k8s.Connection
	before, after *Set
	id            string
	base, next    *Settings
	clearScope    bool
}

func (e Edit) Set() Set {
	if e.after == nil {
		return Set{}
	}
	return *e.after
}

func (e Edit) Discard() {
	e.after.closeDisplaced(e.before)
}

func (r *Runtime) PrepareProfiles(conn *k8s.Connection, target k8s.ProfileTarget, selected map[config.Integration]connections.Selection, rebuild ...config.Integration) Edit {
	edit := r.captureEdit(conn)
	r.prepare(&edit, r.profiles(conn, target, selected), rebuild)
	return edit
}

func (r *Runtime) captureEdit(conn *k8s.Connection) Edit {
	return Edit{runtime: r, conn: conn, before: ownedSetFor(conn), id: k8s.ServingConnectionID()}
}

func (r *Runtime) prepare(edit *Edit, settings Settings, rebuild []config.Integration) {
	edit.clearScope = settings.scopeCleared
	if edit.id == "" {
		return
	}
	edit.after = edit.before
	if len(rebuild) != 0 || edit.before == nil || !sameSettings(settings, edit.before.settings) {
		edit.after = build(edit.conn, settings, edit.before, rebuild)
	}
}

func (e Edit) Commit(persist func() error) error {
	if err := k8s.TryClusterConfiguration(func() error { return e.commitLocked(persist) }); err != nil {
		e.Discard()
		return err
	}
	e.finish()
	return nil
}

func (e *Edit) commitLocked(persist func() error) error {
	if (e.base != nil && e.runtime.Snapshot() != e.base) || k8s.Current() != e.conn || k8s.ServingConnectionID() != e.id || ownedSetFor(e.conn) != e.before {
		return k8s.ErrContextConfigurationBusy
	}
	if err := persist(); err != nil {
		return err
	}
	if e.clearScope {
		e.runtime.ClearLaunchScope()
	}
	if e.next != nil {
		e.runtime.settings.Store(e.next)
	}
	var before io.Closer
	if e.before != nil {
		before = e.before
	}
	if e.after != nil && e.after != e.before && !k8s.ReplaceIntegrations(e.conn, before, e.after) {
		// Retirement won after persistence. The next admission reads the saved settings.
		e.Discard()
		e.before, e.after = nil, nil
	}
	return nil
}

func (e Edit) finish() {
	e.before.closeDisplaced(e.after)
	if e.after != nil {
		e.after.start(e.before)
	}
}

func (r *Runtime) RefreshProfiles() {
	id := k8s.ServingConnectionID()
	if r.Resolver == nil || id == "" {
		return
	}
	conn := k8s.Current()
	before := ownedSetFor(conn)
	target, err := k8s.ProfileTargetFor(conn)
	if err != nil || before == nil {
		return
	}
	settings := r.profiles(conn, target, r.Resolver.ResolveAll(target, false))
	if sameSettings(settings, before.settings) {
		return
	}
	edit := Edit{runtime: r, conn: conn, before: before, id: id}
	r.prepare(&edit, settings, nil)
	_ = edit.Commit(func() error { return nil })
}

func (r *Runtime) PrepareShared(conn *k8s.Connection, base *Settings, update func(*Settings), rebuild ...config.Integration) Edit {
	edit := r.captureEdit(conn)
	next := *base
	update(&next)
	settings := next
	if r.Resolver != nil {
		if before := edit.before; before != nil {
			settings = before.settings
			update(&settings)
		}
	}
	if r.scopeCleared.Load() || !reflect.DeepEqual(next.Metrics, base.Metrics) {
		settings.Scope = prom.WorkloadMetricsScope{}
		settings.scopeCleared = r.scopeConfigured
	}
	r.prepare(&edit, settings, rebuild)
	if !sameSettings(next, *base) {
		edit.next = &next
	}
	edit.base = base
	return edit
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

func sameSettings(a, b Settings) bool {
	if a.relay != b.relay || errorText(a.MetricsError) != errorText(b.MetricsError) ||
		errorText(a.ArgoError) != errorText(b.ArgoError) || errorText(a.CostError) != errorText(b.CostError) {
		return false
	}
	a.relay, b.relay = nil, nil
	a.MetricsError, b.MetricsError = nil, nil
	a.ArgoError, b.ArgoError = nil, nil
	a.CostError, b.CostError = nil, nil
	return reflect.DeepEqual(a, b)
}

func (r *Runtime) SetRelay(relay *prometheus.RelayConfig) error {
	if err := relay.Validate(); err != nil {
		return err
	}
	var edit Edit
	var applyErr error
	k8s.WithClusterConfiguration(func() {
		edit = r.PrepareShared(k8s.Current(), r.Snapshot(), func(s *Settings) { s.relay = relay })
		if err := edit.commitLocked(func() error { return nil }); err != nil {
			edit.Discard()
			edit = Edit{}
			applyErr = err
			return
		}
		if relay != nil && edit.before != nil && edit.after != nil && edit.after.Metrics != nil && edit.after.Metrics == edit.before.Metrics {
			applyErr = edit.after.Metrics.EnableRelay(*relay)
		}
	})
	edit.finish()
	return applyErr
}
