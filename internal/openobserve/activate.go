package openobserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

// IgnoredInClusterMessage is logged once when in-cluster Radar finds an entry.
const IgnoredInClusterMessage = "[openobserve] Ignoring the OpenObserve entry in ~/.radar/config.json: OpenObserve connections are local only"

const (
	cacheTTL       = 5 * time.Minute
	retryInterval  = 30 * time.Second
	uidReadTimeout = 10 * time.Second
	checkingNote   = "Checking whether OpenObserve has this cluster's metrics…"
)

// Target is the kubeconfig context being activated.
type Target struct {
	Context string
	// Fingerprint identifies the cluster behind the context; the cluster UID
	// read for it is remembered under this key.
	Fingerprint string
	Client      kubernetes.Interface
}

type cached struct {
	d  decision
	at time.Time
}

// Activator decides, per context, whether the shared OpenObserve connection
// serves metrics, and configures the global metrics client accordingly.
type Activator struct {
	mu      sync.Mutex
	gen     uint64
	enabled bool // the metrics relay is ours
	uids    map[string]string
	cache   map[string]cached
	ignored sync.Once
	// done, when set, is told each time an asynchronous match finishes; tests.
	done func()

	loadConfig  func() *config.OpenObserveConfig
	inCluster   func() bool
	now         func() time.Time
	resolve     func(context.Context, oo.Connection, config.OpenObserveConfig, string, string) decision
	saveIDMatch func(uid string, m config.OpenObserveMatch)
	retryAfter  time.Duration
}

// NewActivator returns an Activator reading ~/.radar/config.json.
func NewActivator() *Activator {
	return &Activator{
		uids:  map[string]string{},
		cache: map[string]cached{},
		loadConfig: func() *config.OpenObserveConfig {
			c := config.Load().OpenObserve
			if c == nil {
				return nil
			}
			cp := c.Clone()
			return &cp
		},
		inCluster:   k8s.IsInCluster,
		now:         time.Now,
		resolve:     resolve,
		saveIDMatch: saveIDMatch,
		retryAfter:  retryInterval,
	}
}

// Default is the process-wide activator used by local connection activation.
var Default = NewActivator()

// LogIfIgnored logs once when Radar runs in a cluster and the config file
// carries an OpenObserve entry. It reports whether the entry is ignored.
func (a *Activator) LogIfIgnored() bool {
	if !a.inCluster() || a.loadConfig() == nil {
		return false
	}
	a.ignored.Do(func() { log.Print(IgnoredInClusterMessage) })
	return true
}

// Apply configures metrics for t. Only a cluster with a stored match uses
// OpenObserve, and then it does even when a Prometheus URL is set by hand for
// the context: the relay layers over the configured client and the hand-set
// URL returns when the match goes. A remembered decision applies at once;
// otherwise OpenObserve stops serving the previous context right away and the
// match runs in the background, so a context switch never waits on
// OpenObserve. A cluster whose UID isn't known yet gets no "checking" note
// until the UID shows it has a match.
func (a *Activator) Apply(t Target) {
	if a.LogIfIgnored() {
		a.Clear()
		return
	}
	cfg := a.loadConfig()
	if cfg == nil || len(cfg.Matches) == 0 {
		a.Clear()
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gen++
	gen := a.gen
	if uid, ok := a.uids[t.Fingerprint]; ok && t.Fingerprint != "" {
		if match := a.startLocked(gen, t, *cfg, uid); match != nil {
			go func() {
				match()
				a.finished()
			}()
		}
		return
	}
	a.disableLocked()
	prometheus.SetSourceNote(nil)
	go func() {
		defer a.finished()
		a.matchAfterUID(gen, t, *cfg, false)
	}()
}

// matchAfterUID reads t's cluster UID and runs its match. A failed read is
// tried again while gen is current: an unchanged context and settings are
// never applied again on their own, so one failed read would otherwise leave
// OpenObserve off until a restart, a context switch or a save.
func (a *Activator) matchAfterUID(gen uint64, t Target, cfg config.OpenObserveConfig, retry bool) {
	uid, err := a.clusterUID(context.Background(), t)
	a.mu.Lock()
	if a.gen != gen {
		a.mu.Unlock()
		return
	}
	var match func()
	if err != nil {
		if !retry {
			log.Printf("[openobserve] Couldn't read the cluster ID of context %q: %v", k8s.SanitizeForLog(t.Context), err)
		}
		if savedFromContext(cfg, t.Context) {
			prometheus.SetSourceNote(&prom.MetricsSource{Kind: "openobserve", Via: "local", Error: "Couldn't read this cluster's ID to find it in OpenObserve: " + err.Error()})
		}
		time.AfterFunc(a.retryAfter, func() {
			a.mu.Lock()
			current := a.gen == gen
			a.mu.Unlock()
			if current {
				a.matchAfterUID(gen, t, cfg, true)
				a.finished()
			}
		})
	} else {
		match = a.startLocked(gen, t, cfg, uid)
	}
	a.mu.Unlock()
	if match != nil {
		match()
	}
}

// startLocked applies what is known for the cluster with UID uid and returns
// the background match to run, if one is needed. Caller holds a.mu.
func (a *Activator) startLocked(gen uint64, t Target, cfg config.OpenObserveConfig, uid string) func() {
	if _, ok := cfg.Matches[uid]; !ok {
		a.disableLocked()
		prometheus.SetSourceNote(nil)
		return nil
	}
	conn, err := Connection(cfg)
	if err != nil {
		a.disableLocked()
		prometheus.SetSourceNote(&prom.MetricsSource{Kind: "openobserve", Via: "local", Error: "OpenObserve connection in ~/.radar/config.json can't be used: " + err.Error()})
		return nil
	}
	if c, ok := a.cache[cacheKey(uid, connKey(conn), cfg)]; ok && a.now().Sub(c.at) < cacheTTL {
		a.applyLocked(c.d, conn, cfg, uid)
		return nil
	}
	a.disableLocked()
	prometheus.SetSourceNote(&prom.MetricsSource{Kind: "openobserve", Via: "local", Warning: checkingNote, Pending: true})
	return func() { a.run(gen, t, conn, cfg, uid) }
}

// savedFromContext reports whether a stored match was saved from this
// context name, the only hint that a cluster whose UID can't be read has one.
func savedFromContext(cfg config.OpenObserveConfig, contextName string) bool {
	for _, m := range cfg.Matches {
		if contextName != "" && m.Context == contextName {
			return true
		}
	}
	return false
}

func (a *Activator) finished() {
	if a.done != nil {
		a.done()
	}
}

// Refresh forgets remembered decisions and applies t again, after the saved
// connection or a match changed.
func (a *Activator) Refresh(t Target) {
	a.mu.Lock()
	a.cache = map[string]cached{}
	a.mu.Unlock()
	a.Apply(t)
}

// Clear stops OpenObserve from serving metrics: metrics are off for the
// context, the cluster has no match, or the entry is gone.
func (a *Activator) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gen++
	a.disableLocked()
	prometheus.SetSourceNote(nil)
}

func (a *Activator) run(gen uint64, t Target, conn oo.Connection, cfg config.OpenObserveConfig, uid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*matchTimeout)
	d := a.resolve(ctx, conn, cfg, uid, t.Context)
	cancel()

	if d.outcome == outcomeMatched && d.match.By == config.OpenObserveMatchByID {
		if stored := cfg.Matches[uid]; stored.By == config.OpenObserveMatchByID && (stored.Label != d.match.Label || stored.Value != d.match.Value) {
			a.saveIDMatch(uid, config.OpenObserveMatch{Label: d.match.Label, Value: d.match.Value, By: config.OpenObserveMatchByID})
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if d.outcome != outcomeUnreachable {
		a.cache[cacheKey(uid, connKey(conn), cfg)] = cached{d: d, at: a.now()}
	}
	if a.gen != gen {
		return
	}
	a.applyLocked(d, conn, cfg, uid)
	if d.outcome == outcomeUnreachable {
		time.AfterFunc(a.retryAfter, func() {
			a.mu.Lock()
			current := a.gen == gen
			a.mu.Unlock()
			if current {
				a.run(gen, t, conn, cfg, uid)
				a.finished()
			}
		})
	}
}

// ClusterUID returns the kube-system UID of t's cluster, remembered per
// fingerprint.
func (a *Activator) ClusterUID(ctx context.Context, t Target) (string, error) {
	return a.clusterUID(ctx, t)
}

func (a *Activator) clusterUID(ctx context.Context, t Target) (string, error) {
	a.mu.Lock()
	uid, ok := a.uids[t.Fingerprint]
	a.mu.Unlock()
	if ok && t.Fingerprint != "" {
		return uid, nil
	}
	if t.Client == nil {
		return "", errors.New("no Kubernetes client")
	}
	ctx, cancel := context.WithTimeout(ctx, uidReadTimeout)
	defer cancel()
	uid, err := ReadClusterUID(ctx, t.Client)
	if err != nil {
		return "", err
	}
	if t.Fingerprint != "" {
		a.mu.Lock()
		a.uids[t.Fingerprint] = uid
		a.mu.Unlock()
	}
	return uid, nil
}

// ReadClusterUID returns the kube-system namespace UID, the identity
// OpenObserve data is matched by.
func ReadClusterUID(ctx context.Context, client kubernetes.Interface) (string, error) {
	ns, err := client.CoreV1().Namespaces().Get(ctx, metav1.NamespaceSystem, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read namespace kube-system: %w", err)
	}
	if ns.UID == "" {
		return "", errors.New("namespace kube-system has no UID")
	}
	return string(ns.UID), nil
}

// applyLocked configures the metrics client for a decision. Caller holds a.mu.
func (a *Activator) applyLocked(d decision, conn oo.Connection, cfg config.OpenObserveConfig, uid string) {
	key := connKey(conn)
	switch d.outcome {
	case outcomeMatched:
		m := d.match
		h := &health{}
		src := prom.MetricsSource{
			Kind:            "openobserve",
			Via:             "local",
			MatchedBy:       m.By,
			ClusterLabel:    &prom.MetricsSourceLabel{Name: m.Label, Value: m.Value},
			Dialect:         string(m.Dialect),
			Warning:         m.Warning,
			VersionWarning:  d.versionWarning,
			MissingFamilies: familyNames(m.Missing),
		}
		a.enableLocked(prometheus.RelayConfig{
			Key:          "openobserve-local|" + key + "|" + uid + "|" + optionsKey(m.Options) + fmt.Sprintf("|legacy=%v", d.legacyStore),
			Endpoint:     conn.PrometheusURL(),
			Address:      Address(conn),
			Source:       src,
			NewTransport: transportFactory(conn, m.Options, d.legacyStore, h),
			Health:       h.get,
		})
	case outcomeUnreachable:
		stored := cfg.Matches[uid]
		if stored.Value == oo.UnsafeDefaultName {
			// A stored default name never scoped anything: the cluster's own
			// Prometheus, if any, still serves.
			a.disableLocked()
			prometheus.SetSourceNote(&prom.MetricsSource{Kind: "openobserve", Via: "local", Error: d.reason + " Radar couldn't check whether OpenObserve has this cluster's metrics."})
			return
		}
		reason := d.reason
		a.enableLocked(prometheus.RelayConfig{
			Key:      "openobserve-local-unavailable|" + key + "|" + uid + "|" + reason,
			Endpoint: conn.PrometheusURL(),
			Address:  Address(conn),
			Source:   prom.MetricsSource{Kind: "openobserve", Via: "local", MatchedBy: stored.By, ClusterLabel: &prom.MetricsSourceLabel{Name: stored.Label, Value: stored.Value}, Unavailable: true, Error: reason},
			NewTransport: func(time.Duration, int64) prom.Transport {
				return unavailableTransport{address: Address(conn), reason: reason}
			},
			Health: func() (bool, string) { return true, reason },
		})
	default:
		a.disableLocked()
		prometheus.SetSourceNote(&prom.MetricsSource{Kind: "openobserve", Via: "local", Error: d.reason})
	}
}

func (a *Activator) enableLocked(cfg prometheus.RelayConfig) {
	if err := prometheus.EnableRelay(cfg); err != nil {
		log.Printf("[openobserve] Metrics not routed to OpenObserve: %v", err)
		return
	}
	a.enabled = true
	prometheus.SetSourceNote(nil)
}

func (a *Activator) disableLocked() {
	if !a.enabled {
		return
	}
	a.enabled = false
	if prometheus.DisableRelay() {
		prometheus.Prewarm()
	}
}

// Address is what status shows for the connection: the Prometheus API root,
// which carries no credentials.
func Address(conn oo.Connection) string {
	return "OpenObserve " + prom.SafeAddress(conn.PrometheusURL())
}

func familyNames(fs []oo.Family) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, string(f))
	}
	return out
}

func connKey(conn oo.Connection) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{conn.BaseURL, conn.Org, conn.User, conn.Token, conn.CACertPEM}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// cacheKey includes the stored match so a name confirmed with the CLI
// applies on the next activation instead of after the cache expires.
func cacheKey(uid, conn string, cfg config.OpenObserveConfig) string {
	m := cfg.Matches[uid]
	if m.By != config.OpenObserveMatchByName {
		// Cached ID matches are Radar's own record and change no decision.
		m = config.OpenObserveMatch{}
	}
	return strings.Join([]string{uid, conn, m.Label, m.Value}, "|")
}

func optionsKey(o oo.RewriteOptions) string {
	parts := []string{string(o.Dialect), o.CollectorUID, fmt.Sprint(o.GatewayMetricsAllowed)}
	for _, m := range o.Matchers {
		parts = append(parts, url.QueryEscape(m.Name)+"="+url.QueryEscape(m.Value))
	}
	slices.Sort(parts[3:])
	return strings.Join(parts, ",")
}

// saveIDMatch refreshes the cached label of a stored ID match. It never
// creates an entry: a match removed meanwhile stays removed.
func saveIDMatch(uid string, m config.OpenObserveMatch) {
	if _, err := config.Update(func(c *config.Config) {
		if c.OpenObserve == nil {
			return
		}
		existing, ok := c.OpenObserve.Matches[uid]
		if !ok || existing.By != config.OpenObserveMatchByID {
			return
		}
		m.Context = existing.Context
		c.OpenObserve.Matches[uid] = m
	}); err != nil {
		log.Printf("[openobserve] Failed to remember the cluster ID match: %v", err)
	}
}
