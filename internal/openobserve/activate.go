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

// Apply runs when no hand-set Prometheus URL applies to t. A remembered
// decision applies at once; otherwise OpenObserve stops serving the previous
// context right away and the match runs in the background, so a context switch
// never waits on OpenObserve.
func (a *Activator) Apply(t Target) {
	if a.LogIfIgnored() {
		a.Clear()
		return
	}
	cfg := a.loadConfig()
	if cfg == nil {
		a.Clear()
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gen++
	gen := a.gen
	conn, err := Connection(*cfg)
	if err != nil {
		a.disableLocked()
		prometheus.SetSourceNote(&prom.MetricsSource{Kind: "openobserve", Via: "local", Error: "OpenObserve connection in ~/.radar/config.json can't be used: " + err.Error()})
		return
	}
	key := connKey(conn)
	if uid, ok := a.uids[t.Fingerprint]; ok && t.Fingerprint != "" {
		if c, ok := a.cache[cacheKey(uid, key, *cfg)]; ok && a.now().Sub(c.at) < cacheTTL {
			a.applyLocked(c.d, conn, *cfg, uid)
			return
		}
	}
	a.disableLocked()
	prometheus.SetSourceNote(&prom.MetricsSource{Kind: "openobserve", Via: "local", Warning: checkingNote})
	go a.run(gen, t, conn, *cfg)
}

// Clear stops OpenObserve from serving metrics: a hand-set Prometheus URL
// applies, metrics are off for the context, or the entry is gone.
func (a *Activator) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gen++
	a.disableLocked()
	prometheus.SetSourceNote(nil)
}

func (a *Activator) run(gen uint64, t Target, conn oo.Connection, cfg config.OpenObserveConfig) {
	defer func() {
		if a.done != nil {
			a.done()
		}
	}()
	uid, err := a.clusterUID(t)
	if err != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.gen == gen {
			prometheus.SetSourceNote(&prom.MetricsSource{Kind: "openobserve", Via: "local", Error: "Couldn't read this cluster's ID to find it in OpenObserve: " + err.Error()})
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*matchTimeout)
	d := a.resolve(ctx, conn, cfg, uid, t.Context)
	cancel()

	if d.outcome == outcomeMatched && d.match.By == config.OpenObserveMatchByID {
		if stored, ok := cfg.Matches[uid]; !ok || (stored.By == config.OpenObserveMatchByID && stored.Label != d.match.Label) {
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
				a.run(gen, t, conn, cfg)
			}
		})
	}
}

func (a *Activator) clusterUID(t Target) (string, error) {
	a.mu.Lock()
	uid, ok := a.uids[t.Fingerprint]
	a.mu.Unlock()
	if ok && t.Fingerprint != "" {
		return uid, nil
	}
	if t.Client == nil {
		return "", errors.New("no Kubernetes client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), uidReadTimeout)
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
		stored, ok := cfg.Matches[uid]
		if !ok || stored.Value == oo.UnsafeDefaultName {
			// Never matched: the cluster's own Prometheus, if any, still serves.
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

func saveIDMatch(uid string, m config.OpenObserveMatch) {
	if _, err := config.Update(func(c *config.Config) {
		if c.OpenObserve == nil {
			return
		}
		if existing, ok := c.OpenObserve.Matches[uid]; ok && existing.By == config.OpenObserveMatchByName {
			// An ID match supersedes a confirmed name, but the user's
			// confirmation is kept in case the ID label disappears.
			return
		}
		if c.OpenObserve.Matches == nil {
			c.OpenObserve.Matches = map[string]config.OpenObserveMatch{}
		}
		c.OpenObserve.Matches[uid] = m
	}); err != nil {
		log.Printf("[openobserve] Failed to remember the cluster ID match: %v", err)
	}
}
