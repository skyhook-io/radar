package opencost

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

// OpenCost's metrics live in a store that may hold several clusters. Each
// cost endpoint checks the metrics it actually reads, as one joined view:
// prices, allocations, volumes and ownership must all land on this cluster or
// the costs are withheld (ReasonClusterScope). The usage metrics behind
// efficiency and idle are checked on top of those; when only they fail, the
// costs still show and efficiency reads unavailable. Kubecost answers are
// already filtered by cluster ID and never come here.

const (
	// The current-cost queries read [1h:1m] allocation subqueries and
	// rate(...[1h]) usage; each subquery step's plain selector looks back
	// the store's implicit 5 minutes further.
	costQueryWindow      = time.Hour
	costImplicitLookback = 5 * time.Minute
	costCurrentSpan      = costQueryWindow + costImplicitLookback
)

var (
	scopeResolverMu       sync.RWMutex
	scopeResolverOverride prometheuspkg.ChartScopeResolver
)

// SetScopeResolverForTesting replaces how cost endpoints decide their cluster
// scope and returns a func that restores the default.
func SetScopeResolverForTesting(resolve prometheuspkg.ChartScopeResolver) func() {
	scopeResolverMu.Lock()
	previous := scopeResolverOverride
	scopeResolverOverride = resolve
	scopeResolverMu.Unlock()
	return func() {
		scopeResolverMu.Lock()
		scopeResolverOverride = previous
		scopeResolverMu.Unlock()
	}
}

// PromCost runs the metrics-store cost paths kept to this cluster. Client
// decides the cluster scope; Store runs the cost queries (MCP passes its
// bounded client).
type PromCost struct {
	Client *prometheuspkg.Client
	Store  *prom.Client
}

// NewPromCost serves the REST routes from client's own store connection.
func NewPromCost(client *prometheuspkg.Client) PromCost {
	return PromCost{Client: client, Store: client.Prom()}
}

func (p PromCost) resolver() prometheuspkg.ChartScopeResolver {
	scopeResolverMu.RLock()
	override := scopeResolverOverride
	scopeResolverMu.RUnlock()
	if override != nil {
		return override
	}
	return prometheuspkg.ResolverFor(p.Client, k8s.GetResourceCache())
}

// costSubject is what one endpoint's check is narrowed to and anchored on.
type costSubject struct {
	namespace   string
	pods        []string
	nodes       []string
	replicaSets []string
	volumes     []string
}

func clusterCostSubject(cache *k8s.ResourceCache) costSubject {
	return costSubject{
		pods:    prometheuspkg.ClusterPodAnchors(cache),
		nodes:   prometheuspkg.NodeAnchors(cache),
		volumes: prometheuspkg.PersistentVolumeAnchors(cache),
	}
}

func namespaceCostSubject(cache *k8s.ResourceCache, namespace string) costSubject {
	return costSubject{
		namespace:   namespace,
		pods:        prometheuspkg.NamespacePodAnchors(cache, namespace),
		nodes:       prometheuspkg.NodeAnchors(cache),
		replicaSets: prometheuspkg.NamespaceReplicaSetAnchors(cache, namespace),
	}
}

// costViewMetric narrows one metric to the subject the way the cost queries
// select it and anchors it on the objects it is keyed by.
func costViewMetric(name string, s costSubject) prometheuspkg.ViewMetric {
	namespaced := ""
	if s.namespace != "" {
		namespaced = "namespace=" + strconv.Quote(s.namespace)
	}
	switch name {
	case pkgopencost.MetricNodeTotalHourlyCost, pkgopencost.MetricNodeCPUHourlyCost, pkgopencost.MetricNodeRAMHourlyCost,
		pkgopencost.MetricNodeGPUCount, pkgopencost.MetricNodeGPUHourlyCost:
		return prometheuspkg.ViewMetric{Name: name, AnchorLabel: "node", AnchorValues: s.nodes}
	case pkgopencost.MetricPVHourlyCost, pkgopencost.MetricPVCapacityBytes, pkgopencost.MetricPVClaimRef:
		return prometheuspkg.ViewMetric{Name: name, AnchorLabel: "persistentvolume", AnchorValues: s.volumes}
	case pkgopencost.MetricReplicaSetOwner:
		return prometheuspkg.ViewMetric{Name: name, Subject: namespaced, AnchorLabel: "replicaset", AnchorValues: s.replicaSets}
	case pkgopencost.MetricCPUUsage, pkgopencost.MetricMemoryWorkingSet, pkgopencost.MetricPodOwner:
		return prometheuspkg.ViewMetric{Name: name, Subject: namespaced, AnchorLabel: "pod", AnchorValues: s.pods}
	}
	// OpenCost's allocations and cost totals carry the Pod's namespace in
	// namespace or, when the scrape renamed it, exported_namespace. A subject
	// can't say "either", so they are checked across the whole store.
	return prometheuspkg.ViewMetric{Name: name, AnchorLabel: "pod", AnchorValues: s.pods}
}

func costScopeRequest(view string, metrics []string, s costSubject, span time.Duration) prometheuspkg.ViewScopeRequest {
	req := prometheuspkg.ViewScopeRequest{View: view, Joined: true, Span: span, Namespace: s.namespace}
	for _, name := range metrics {
		req.Metrics = append(req.Metrics, costViewMetric(name, s))
	}
	return req
}

func wireCostScope(scope prometheuspkg.ViewScope) *pkgopencost.ClusterScope {
	wire := prometheuspkg.ClusterScopeOf(scope)
	if wire == nil {
		return nil
	}
	return &pkgopencost.ClusterScope{State: string(wire.State), Labels: maps.Clone(wire.Labels), Note: wire.Note}
}

// resolveCostScope decides one endpoint's scope. A non-empty reason means
// the costs can't be shown: ReasonClusterScope when they couldn't be kept to
// this cluster (the returned scope's ClusterScope says why), ReasonQueryError
// when the caller's context ended first.
func (p PromCost) resolveCostScope(ctx context.Context, view string, costs, usage []string, s costSubject, span time.Duration) (pkgopencost.StoreScope, string) {
	resolve := p.resolver()
	core, err := prometheuspkg.ResolveChartScope(ctx, resolve, costScopeRequest(view, costs, s, span))
	if err != nil {
		return pkgopencost.StoreScope{}, pkgopencost.ReasonQueryError
	}
	if core.Withheld {
		return pkgopencost.StoreScope{ClusterScope: wireCostScope(core)}, pkgopencost.ReasonClusterScope
	}
	if len(usage) == 0 {
		return pkgopencost.StoreScope{Filter: core.Filter, Strict: true, ClusterScope: wireCostScope(core)}, ""
	}
	full, err := prometheuspkg.ResolveChartScope(ctx, resolve, costScopeRequest(view+"_usage", slices.Concat(costs, usage), s, span))
	if err != nil {
		return pkgopencost.StoreScope{}, pkgopencost.ReasonQueryError
	}
	if full.Withheld {
		return pkgopencost.StoreScope{
			Filter: core.Filter, Strict: true, UsageWithheld: true,
			ClusterScope: wireCostScope(core), UsageClusterScope: wireCostScope(full),
		}, ""
	}
	return pkgopencost.StoreScope{Filter: full.Filter, Strict: true, ClusterScope: wireCostScope(full)}, ""
}

// Summary is the cost summary. A namespace-filtered caller skips the node
// cost, so its check leaves those metrics out too.
func (p PromCost) Summary(ctx context.Context, opts pkgopencost.SummaryOptions) *pkgopencost.CostSummary {
	costs, usage := pkgopencost.SummaryMetrics(opts.SkipNodeCost)
	scope, reason := p.resolveCostScope(ctx, "cost_summary", costs, usage, clusterCostSubject(k8s.GetResourceCache()), costCurrentSpan)
	if reason != "" {
		currency := opts.Currency
		if currency == "" {
			currency = pkgopencost.DefaultCurrency
		}
		return &pkgopencost.CostSummary{Available: false, Reason: reason, Currency: currency, ClusterScope: scope.ClusterScope}
	}
	opts.Scope = scope
	return pkgopencost.ComputeCostSummaryFromProm(ctx, p.Store, opts)
}

// Workloads is one namespace's workload costs.
func (p PromCost) Workloads(ctx context.Context, namespace string, owners pkgopencost.PodOwnerLookup) *pkgopencost.WorkloadCostResponse {
	costs, usage := pkgopencost.WorkloadsMetrics()
	scope, reason := p.resolveCostScope(ctx, "cost_workloads", costs, usage, namespaceCostSubject(k8s.GetResourceCache(), namespace), costCurrentSpan)
	if reason != "" {
		return &pkgopencost.WorkloadCostResponse{Namespace: namespace, Available: false, Reason: reason, ClusterScope: scope.ClusterScope}
	}
	return pkgopencost.ComputeWorkloadsFromPromScoped(ctx, p.Store, namespace, owners, scope)
}

// Nodes is the per-node cost breakdown.
func (p PromCost) Nodes(ctx context.Context) *pkgopencost.NodeCostResponse {
	scope, reason := p.resolveCostScope(ctx, "cost_nodes", pkgopencost.NodeMetrics(), nil, costSubject{nodes: prometheuspkg.NodeAnchors(k8s.GetResourceCache())}, costImplicitLookback)
	if reason != "" {
		return &pkgopencost.NodeCostResponse{Available: false, Reason: reason, ClusterScope: scope.ClusterScope}
	}
	return pkgopencost.ComputeNodeCostsScoped(ctx, p.Store, scope)
}

func trendSpan(rangeStr string) time.Duration {
	return pkgopencost.TrendRangeDuration(rangeStr) + costCurrentSpan
}

// Trend is the per-namespace cost trend.
func (p PromCost) Trend(ctx context.Context, opts pkgopencost.TrendPromOptions) *pkgopencost.CostTrendResponse {
	cache := k8s.GetResourceCache()
	subject := costSubject{pods: prometheuspkg.ClusterPodAnchors(cache), nodes: prometheuspkg.NodeAnchors(cache)}
	scope, reason := p.resolveCostScope(ctx, "cost_trend", pkgopencost.TrendMetrics(), nil, subject, trendSpan(opts.Range))
	if reason != "" {
		return &pkgopencost.CostTrendResponse{Available: false, Reason: reason, Range: opts.Range, ClusterScope: scope.ClusterScope}
	}
	opts.Scope = scope
	return pkgopencost.ComputeCostTrendFromProm(ctx, p.Store, opts)
}

// WorkloadTrend is one workload's cost trend.
func (p PromCost) WorkloadTrend(ctx context.Context, opts pkgopencost.WorkloadTrendOptions) *pkgopencost.WorkloadCostTrendResponse {
	scope, reason := p.resolveCostScope(ctx, "cost_workload_trend", pkgopencost.WorkloadTrendMetrics(opts.Kind), nil,
		namespaceCostSubject(k8s.GetResourceCache(), opts.Namespace), trendSpan(opts.Range))
	if reason != "" {
		kind, _ := pkgopencost.CanonicalWorkloadKind(opts.Kind)
		return &pkgopencost.WorkloadCostTrendResponse{Namespace: opts.Namespace, Kind: kind, Name: opts.Name, Range: opts.Range, Reason: reason, ClusterScope: scope.ClusterScope}
	}
	opts.Scope = scope
	return pkgopencost.ComputeWorkloadCostTrendFromProm(ctx, p.Store, opts)
}

// ApplicationTrend is an application's cost trend, each namespace checked on
// its own workloads.
func (p PromCost) ApplicationTrend(ctx context.Context, opts pkgopencost.ApplicationTrendOptions) *pkgopencost.ApplicationCostTrendResponse {
	byNamespace := map[string][]pkgopencost.ApplicationWorkloadRef{}
	for _, ref := range opts.Workloads {
		byNamespace[ref.Namespace] = append(byNamespace[ref.Namespace], ref)
	}
	cache := k8s.GetResourceCache()
	span := trendSpan(opts.Range)
	opts.ScopeFor = func(namespace string) (pkgopencost.StoreScope, string) {
		return p.resolveCostScope(ctx, "cost_application_trend", pkgopencost.ApplicationTrendMetrics(byNamespace[namespace]), nil,
			namespaceCostSubject(cache, namespace), span)
	}
	return pkgopencost.ComputeApplicationCostTrendFromProm(ctx, p.Store, opts)
}

// ApplicationClusterScope picks the scope an application's per-namespace
// workload costs report: a withheld namespace's, else the first one checked.
func ApplicationClusterScope(namespaceCosts map[string]*pkgopencost.WorkloadCostResponse) *pkgopencost.ClusterScope {
	var first *pkgopencost.ClusterScope
	for _, namespace := range slices.Sorted(maps.Keys(namespaceCosts)) {
		resp := namespaceCosts[namespace]
		if resp == nil || resp.ClusterScope == nil {
			continue
		}
		if resp.Reason == pkgopencost.ReasonClusterScope {
			return resp.ClusterScope
		}
		if first == nil {
			first = resp.ClusterScope
		}
	}
	return first
}
