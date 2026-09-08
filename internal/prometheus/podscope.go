package prometheus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

// OwnerCoverage says which source established the pods a metrics query
// covers. ksm_history: kube-state-metrics attributed pods to the workload
// during the window, so replaced pods are included. current_pods: only the
// pods the workload controls now, resolved from the cluster. none: neither
// source named a pod, and a query built from the scope matches nothing.
type OwnerCoverage string

const (
	OwnerCoverageKSMHistory  OwnerCoverage = "ksm_history"
	OwnerCoverageCurrentPods OwnerCoverage = "current_pods"
	OwnerCoverageNone        OwnerCoverage = "none"
)

// PodScope is the resolved membership of one workload for one window: the
// pods it controls now, whether kube-state-metrics can attribute pods over
// the window, and the selection every metrics query over it must use.
type PodScope struct {
	Kind      string
	Namespace string
	Name      string
	Window    time.Duration

	// CurrentPods are the controlled pods now, sorted, cut to the caller's
	// cap; CurrentTotal is the uncapped count.
	CurrentPods  []string
	CurrentTotal int
	// ObservedPods is how many pods kube-state-metrics attributed to the
	// workload during the window (plus the trailing hour a restarts query
	// reads), when Coverage is ksm_history.
	ObservedPods int
	Coverage     OwnerCoverage
	Selection    prom.PodSelection
	// ProbeErr is set when the ownership-history probe could not be
	// evaluated, so a current_pods answer may be a fallback rather than the
	// truth about history. It is reported, never hidden.
	ProbeErr error
}

// Partial reports that the current-pod list was cut by the cap. It is about
// the list, not the charted population: with ownership history the query
// covers every pod the workload owned and the list is only a count the
// caller reports, but a cut list is still a cut list and must say so.
func (s PodScope) Partial() bool {
	return s.CurrentTotal > len(s.CurrentPods)
}

// scopeQuerier is the one Prometheus call the resolver needs.
type scopeQuerier interface {
	Query(ctx context.Context, query string) (*prom.QueryResult, error)
}

// ErrPodScopeUnsupportedKind is returned for kinds whose pods no source can
// attribute (a Service, a Node).
var ErrPodScopeUnsupportedKind = errors.New("pod scope: unsupported kind")

// scopeProbeTTL bounds how long a resolved scope is reused for the same
// workload and window. Membership changes on a rollout and the probe is one
// instant query, so a short reuse absorbs the six charts a page opens at
// once without serving stale pods for long.
const scopeProbeTTL = 2 * time.Minute

type scopeCacheEntry struct {
	querier  scopeQuerier
	scope    PodScope
	resolved time.Time
}

var (
	scopeCacheMu sync.Mutex
	scopeCache   = map[string]scopeCacheEntry{}
	scopeFlight  singleflight.Group
)

// ResolvePodScope establishes membership for kind/namespace/name over a
// window ending now. Current pods come from the cluster by controller
// ownership (k8s.WorkloadPods); history comes from kube-state-metrics when
// its ownership series name at least one pod for the workload in the window.
// maxPods caps the current-pods form only: an ownership join needs no list.
//
// A workload the cache cannot see (denied listers) is an error, never an
// empty scope, so "no pods" is only ever said when it is true.
func ResolvePodScope(ctx context.Context, querier scopeQuerier, cache *k8s.ResourceCache, kind, namespace, name string, window time.Duration, maxPods int) (PodScope, error) {
	if k := strings.ToLower(strings.TrimSpace(kind)); k == "pod" || k == "pods" {
		// A Pod is its own scope: exactly one name, no ownership to resolve.
		return PodScope{
			Kind: kind, Namespace: namespace, Name: name, Window: window,
			CurrentPods: []string{name}, CurrentTotal: 1,
			Coverage:  OwnerCoverageCurrentPods,
			Selection: prom.SelectPods(namespace, []string{name}),
		}, nil
	}
	if _, ok := prom.OwnerHopFor(kind); !ok {
		return PodScope{}, fmt.Errorf("%w: %s", ErrPodScopeUnsupportedKind, kind)
	}
	// The key carries everything a resolved scope depends on: the workload,
	// the window, the cap, the cluster whose pods it named, and the
	// Prometheus connection that answered. The cluster is not implied by the
	// connection — a context switch keeps the same client pointer — so both
	// are in the key, and the context switch drops the cache outright.
	key := strings.Join([]string{
		strings.ToLower(kind), namespace, name, window.String(), fmt.Sprint(maxPods),
		k8s.GetConnectionStatus().Context, fmt.Sprint(querierGeneration(querier)),
		fmt.Sprintf("%p", querier),
	}, "\x00")
	scopeCacheMu.Lock()
	entry, ok := scopeCache[key]
	scopeCacheMu.Unlock()
	if ok && entry.querier == querier && time.Since(entry.resolved) < scopeProbeTTL {
		return entry.scope, nil
	}
	v, err, _ := scopeFlight.Do(key, func() (any, error) {
		scope, err := resolvePodScope(ctx, querier, cache, kind, namespace, name, window, maxPods)
		if err != nil {
			return PodScope{}, err
		}
		// A probe the caller's own deadline cut short says nothing about the
		// cluster, so it is answered but never remembered.
		if scope.ProbeErr == nil || !isContextError(scope.ProbeErr) {
			scopeCacheMu.Lock()
			scopeCache[key] = scopeCacheEntry{querier: querier, scope: scope, resolved: time.Now()}
			scopeCacheMu.Unlock()
		}
		return scope, nil
	})
	if err != nil {
		return PodScope{}, err
	}
	return v.(PodScope), nil
}

// ResetPodScopeCache drops every resolved scope. The server calls it on a
// context switch, where the pods, the ownership history and the Prometheus
// behind them all change at once.
func ResetPodScopeCache() {
	scopeCacheMu.Lock()
	scopeCache = map[string]scopeCacheEntry{}
	scopeCacheMu.Unlock()
}

func resolvePodScope(ctx context.Context, querier scopeQuerier, cache *k8s.ResourceCache, kind, namespace, name string, window time.Duration, maxPods int) (PodScope, error) {
	scope := PodScope{Kind: kind, Namespace: namespace, Name: name, Window: window, Coverage: OwnerCoverageNone}

	current, err := k8s.WorkloadPodNames(cache, kind, namespace, name)
	if err != nil {
		return PodScope{}, err
	}
	scope.CurrentTotal = len(current)
	if maxPods > 0 && len(current) > maxPods {
		current = current[:maxPods]
	}
	scope.CurrentPods = current

	// The count describes the window the caller asked about, so the caption
	// can state it plainly; the per-category lookback that keeps a replaced
	// pod's trailing rate attributed lives in the chart query, not here.
	ref := prom.WorkloadRef{Kind: kind, Namespace: namespace, Name: name}
	observed, probeErr := probeOwnedPods(ctx, querier, ref, window)
	scope.ProbeErr = probeErr
	switch {
	case observed > 0:
		scope.Coverage = OwnerCoverageKSMHistory
		scope.ObservedPods = observed
		scope.Selection = prom.SelectOwner(namespace, kind, name)
	case len(current) > 0:
		scope.Coverage = OwnerCoverageCurrentPods
		scope.Selection = prom.SelectPods(namespace, current)
	default:
		scope.Selection = prom.PodSelection{Namespace: namespace}
	}
	return scope, nil
}

// probeOwnedPods counts the pods kube-state-metrics attributed to the
// workload across the window. One instant query: the ownership expression
// carries both edges, so nothing about the hop is frozen into a name list
// that a later chart step could not re-check.
func probeOwnedPods(ctx context.Context, querier scopeQuerier, ref prom.WorkloadRef, lookback time.Duration) (int, error) {
	query := prom.BuildOwnedPodsProbe(ref, lookback)
	if query == "" || querier == nil {
		return 0, nil
	}
	res, err := querier.Query(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("ownership probe: %w", err)
	}
	count := firstValue(res)
	if count == nil || *count <= 0 {
		return 0, nil
	}
	return int(*count), nil
}

func isContextError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// querierGeneration identifies the connection a scope was resolved against.
// The client singleton keeps its pointer across a reconnect, a manual URL or
// a header change, so pointer identity alone would reuse another cluster's
// membership; the discovery generation moves whenever any of those do.
func querierGeneration(querier scopeQuerier) uint64 {
	if gen, ok := querier.(interface{ DiscoveryGeneration() uint64 }); ok {
		return gen.DiscoveryGeneration()
	}
	return 0
}
