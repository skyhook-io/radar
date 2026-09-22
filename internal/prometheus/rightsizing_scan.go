package prometheus

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
)

type RightsizingScanState string

const (
	RightsizingScanComplete    RightsizingScanState = "complete"
	RightsizingScanPartial     RightsizingScanState = "partial"
	RightsizingScanUnavailable RightsizingScanState = "unavailable"
	rightsizingScanBatchSize                        = 50
)

// RightsizingScanKind pairs a workload kind with the API resource its informer
// and SubjectAccessReviews use.
type RightsizingScanKind struct {
	Kind     string
	Resource string
}

// RightsizingScanKinds is the canonical set of kinds a scan walks. Both the
// REST handler and the MCP tool build their scope from this one list: a kind
// present on one surface and missing on the other is silently absent from
// results rather than reported as restricted.
var RightsizingScanKinds = []RightsizingScanKind{
	{Kind: "Deployment", Resource: "deployments"},
	{Kind: "StatefulSet", Resource: "statefulsets"},
	{Kind: "DaemonSet", Resource: "daemonsets"},
}

// ScanKindResource maps a workload kind to the API resource its informer and
// SubjectAccessReviews use. Authorization sites go through this rather than
// pluralizing the kind, so a resource whose plural is not kind+"s" cannot
// silently authorize against a resource that does not exist.
func ScanKindResource(kind string) string {
	for _, k := range RightsizingScanKinds {
		if strings.EqualFold(k.Kind, kind) {
			return k.Resource
		}
	}
	return ""
}

// ScanAuthorizer answers what the caller's identity may list. The REST handler
// and the MCP tool differ only in how they run a SubjectAccessReview, so the
// scope policy itself — which kinds are restricted, and how a partial namespace
// answer is recorded — lives here rather than being written once per surface.
type ScanAuthorizer interface {
	// CanListAllNamespaces reports cluster-wide list access for a resource.
	CanListAllNamespaces(resource string) bool
	// FilterNamespaces returns the subset of namespaces the identity can list.
	FilterNamespaces(resource string, namespaces []string) []string
}

// ResolveScanScope builds the scan scope for an identity. A nil namespaces
// means "every namespace"; a non-nil one is the already-resolved request. A
// kind readable in only some of the requested namespaces is recorded as
// restricted, so the scan reports a narrowed answer as partial rather than
// as a complete one over fewer workloads.
func ResolveScanScope(namespaces []string, authz ScanAuthorizer) RightsizingScanScope {
	scope := RightsizingScanScope{
		NamespacesByKind: make(map[string][]string, len(RightsizingScanKinds)),
	}
	for _, workloadKind := range RightsizingScanKinds {
		if namespaces == nil {
			if authz.CanListAllNamespaces(workloadKind.Resource) {
				scope.NamespacesByKind[workloadKind.Kind] = nil
			} else {
				scope.RestrictedKinds = append(scope.RestrictedKinds, workloadKind.Kind)
			}
			continue
		}
		allowed := authz.FilterNamespaces(workloadKind.Resource, namespaces)
		if len(allowed) > 0 {
			scope.NamespacesByKind[workloadKind.Kind] = allowed
		}
		if len(allowed) < len(namespaces) {
			scope.RestrictedKinds = append(scope.RestrictedKinds, workloadKind.Kind)
		}
	}
	return scope
}

type RightsizingScanScope struct {
	NamespacesByKind map[string][]string
	RestrictedKinds  []string
}

// deniedKinds are the restricted kinds readable in no requested namespace. A
// kind readable in some of them is narrowed, not unreadable.
func (s RightsizingScanScope) deniedKinds() []string {
	var denied []string
	for _, kind := range s.RestrictedKinds {
		if _, readable := s.NamespacesByKind[kind]; !readable {
			denied = append(denied, kind)
		}
	}
	return denied
}

type RightsizingScanWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RightsizingScanCoverage struct {
	WorkloadsDiscovered int      `json:"workloadsDiscovered"`
	WorkloadsEvaluated  int      `json:"workloadsEvaluated"`
	WorkloadsWithData   int      `json:"workloadsWithData"`
	Batches             int      `json:"batches"`
	CompletedBatches    int      `json:"completedBatches"`
	AttemptedBatches    int      `json:"attemptedBatches"`
	RestrictedKinds     []string `json:"restrictedKinds,omitempty"`
	UnavailableKinds    []string `json:"unavailableKinds,omitempty"`

	// PartiallyCachedKinds are kinds whose informer covers only some
	// namespaces, so a scope asking for "all namespaces" silently reads a
	// subset. Without this a namespace-scoped Radar reports a cluster scan of
	// one namespace as complete.
	PartiallyCachedKinds []string `json:"partiallyCachedKinds,omitempty"`

	// DaemonSetsWithoutNodes counts DaemonSets left out because their node
	// selector matches no node: no pods, nothing to size, and on managed
	// clusters dozens of them would otherwise fill the ranked list.
	DaemonSetsWithoutNodes int `json:"daemonSetsWithoutNodes,omitempty"`
	// SkippedDaemonSets names them as namespace/name, sorted, so a caller can
	// read one's retained history directly.
	SkippedDaemonSets []string `json:"-"`

	// ScannedNamespacesByKind is the requested scope narrowed to what the
	// informer cache holds, once the scan reaches the cache. A caller naming
	// the namespaces that were scanned must read them here rather than from
	// its own request, which the cache can be narrower than.
	ScannedNamespacesByKind map[string][]string `json:"-"`

	deniedKinds []string
}

type RightsizingScanWorkload struct {
	Kind         string           `json:"kind"`
	Namespace    string           `json:"namespace"`
	Name         string           `json:"name"`
	Replicas     int              `json:"replicas"`
	ScaledToZero bool             `json:"scaledToZero"`
	ManagedBy    *WorkloadManager `json:"managedBy,omitempty"`
	Rows         []RightsizingRow `json:"rows"`
}

type RightsizingScanResponse struct {
	RightsizingScanProgress
	State     RightsizingScanState      `json:"state"`
	ScannedAt time.Time                 `json:"scannedAt"`
	Window    string                    `json:"window"`
	Source    string                    `json:"source"`
	Coverage  RightsizingScanCoverage   `json:"coverage"`
	Workloads []RightsizingScanWorkload `json:"workloads"`
	Warnings  []RightsizingScanWarning  `json:"warnings,omitempty"`
	Reason    string                    `json:"reason,omitempty"`
}

type rightsizingScanQuerier interface {
	Query(context.Context, string) (*prom.QueryResult, error)
	QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (*prom.QueryResult, error)
}

type scanWorkload struct {
	kind      string
	namespace string
	name      string
	replicas  int
	workload  rightsizingWorkload
}

type scanKey struct {
	namespace string
	kind      string
	workload  string
	container string
}

type scanBatchEvidence struct {
	cpu          map[scanKey][]float64
	memory       map[scanKey][]float64
	throttle     map[scanKey][]float64
	restarts     map[scanKey]float64
	terminations map[scanKey]terminationEvidence
	errors       map[string]error
}

func runRightsizingScan(ctx context.Context, scope RightsizingScanScope, client *Client, cache *k8s.ResourceCache, publish func(RightsizingScanResponse)) RightsizingScanResponse {
	resp := newRightsizingScanResponse(time.Now().UTC(), scope)
	if client == nil {
		resp.Reason = "prometheus_unavailable"
		return resp
	}
	if cache == nil {
		resp.Reason = "resource_cache_unavailable"
		return resp
	}
	effective, partiallyCached := clampScopeToCacheCoverage(cache, scope.NamespacesByKind)
	resp.Coverage.PartiallyCachedKinds = partiallyCached
	resp.Coverage.ScannedNamespacesByKind = effective
	workloads, unavailable, skippedDaemonSets := snapshotScanWorkloads(ctx, cache, effective)
	resp.Coverage.UnavailableKinds = unavailable
	resp.Coverage.DaemonSetsWithoutNodes = len(skippedDaemonSets)
	sort.Strings(skippedDaemonSets)
	resp.Coverage.SkippedDaemonSets = skippedDaemonSets
	resp.Coverage.WorkloadsDiscovered = len(workloads)
	publish(resp)
	rows := 0
	for _, workload := range workloads {
		rows += 2 * len(workload.workload.containers)
	}
	if rows > rightsizingScanMaxRows {
		resp.Reason = "scan_capacity_exceeded"
		return resp
	}
	if len(workloads) == 0 {
		return computeRightsizingScanProgress(ctx, nil, workloads, resp, publish)
	}
	querier, err := client.newScanQuerier(ctx, resp.ScannedAt.Truncate(rightsizingStep))
	if err != nil {
		resp.Reason = "prometheus_unavailable"
		if ctx.Err() == nil {
			appendScanWarning(&resp, "prometheus_unavailable", err.Error())
		}
		return resp
	}
	return computeRightsizingScanProgress(ctx, querier, workloads, resp, publish)
}

// scanCacheCoverage is the slice of the resource cache the scope clamp needs.
// Narrowed to an interface so the clamp is testable without standing up a real
// informer cache.
type scanCacheCoverage interface {
	IsKindClusterWide(resource string) bool
	KindNamespaces(resource string) []string
}

// clampScopeToCacheCoverage narrows a requested scope to what the informers
// actually hold. A nil namespace list means "every namespace" to the listers,
// but a per-kind namespace-scoped informer only ever held a subset — reading it
// as authoritative turns a one-namespace cache into a reported cluster scan.
// Kinds narrowed this way are returned so the caller can mark the scan partial.
func clampScopeToCacheCoverage(cache scanCacheCoverage, scopes map[string][]string) (map[string][]string, []string) {
	if len(scopes) == 0 {
		return scopes, nil
	}
	effective := make(map[string][]string, len(scopes))
	var partial []string
	for kind, namespaces := range scopes {
		resource := ScanKindResource(kind)
		effective[kind] = namespaces
		if resource == "" || cache.IsKindClusterWide(resource) {
			continue
		}
		covered := cache.KindNamespaces(resource)
		if len(covered) == 0 {
			// Disabled informer: snapshotScanWorkloads reports it as
			// unavailable via its nil lister, which is the stronger signal.
			continue
		}
		if namespaces == nil {
			effective[kind] = covered
			partial = appendUniqueSorted(partial, kind)
			continue
		}
		narrowed := intersectNamespaces(namespaces, covered)
		effective[kind] = narrowed
		if len(narrowed) < len(namespaces) {
			partial = appendUniqueSorted(partial, kind)
		}
	}
	return effective, partial
}

func intersectNamespaces(requested, covered []string) []string {
	set := make(map[string]struct{}, len(covered))
	for _, ns := range covered {
		set[ns] = struct{}{}
	}
	out := make([]string, 0, len(requested))
	for _, ns := range requested {
		if _, ok := set[ns]; ok {
			out = append(out, ns)
		}
	}
	return out
}

func newRightsizingScanResponse(now time.Time, scope RightsizingScanScope) RightsizingScanResponse {
	restricted := append([]string(nil), scope.RestrictedKinds...)
	sort.Strings(restricted)
	return RightsizingScanResponse{
		State: RightsizingScanUnavailable, ScannedAt: now, Window: "7d", Source: "radar",
		Coverage: RightsizingScanCoverage{
			RestrictedKinds: restricted, deniedKinds: scope.deniedKinds(),
			// Seeded with the request so the early returns below — no client,
			// no cache — report the scope as unnarrowed rather than as empty.
			ScannedNamespacesByKind: scope.NamespacesByKind,
		},
		Workloads: []RightsizingScanWorkload{},
	}
}

func computeRightsizingScan(ctx context.Context, client rightsizingScanQuerier, workloads []scanWorkload, resp RightsizingScanResponse) RightsizingScanResponse {
	return computeRightsizingScanProgress(ctx, client, workloads, resp, func(RightsizingScanResponse) {})
}

func computeRightsizingScanProgress(ctx context.Context, client rightsizingScanQuerier, workloads []scanWorkload, resp RightsizingScanResponse, publish func(RightsizingScanResponse)) RightsizingScanResponse {
	sortScanWorkloads(workloads)
	resp.Coverage.WorkloadsDiscovered = len(workloads)
	if len(workloads) == 0 {
		// Unreadable and partially-cached are counted separately. A kind whose
		// informer covers only some namespaces is still readable, so folding it
		// in here would report "every workload kind is unreadable" for a
		// namespace-scoped Radar whose covered namespaces simply hold none.
		unreadable := countDistinct(resp.Coverage.deniedKinds, resp.Coverage.UnavailableKinds)
		narrowed := countDistinct(resp.Coverage.RestrictedKinds, resp.Coverage.UnavailableKinds, resp.Coverage.PartiallyCachedKinds)
		switch {
		case unreadable >= len(RightsizingScanKinds):
			resp.State = RightsizingScanUnavailable
			resp.Reason = "workload_kinds_unavailable"
		case narrowed > 0:
			resp.State = RightsizingScanPartial
			resp.Reason = "limited_scope_no_workloads"
		case resp.Coverage.DaemonSetsWithoutNodes > 0:
			// Workloads exist; every one was skipped on purpose, which
			// no_workloads' "found none" would contradict.
			resp.State = RightsizingScanComplete
			resp.Reason = "only_daemonsets_without_nodes"
		default:
			resp.State = RightsizingScanComplete
			resp.Reason = "no_workloads"
		}
		return resp
	}

	ksm, err := client.Query(ctx, `count(kube_pod_owner)`)
	if err != nil {
		resp.Reason = "owner_metrics_query_failed"
		if ctx.Err() == nil {
			appendScanWarning(&resp, "owner_metrics_query_failed", err.Error())
		}
		return resp
	}
	if firstValue(ksm) == nil || *firstValue(ksm) <= 0 {
		resp.Reason = "owner_metrics_missing"
		return resp
	}
	replicaSetOwnersQueryFailed := false
	if hasScanKind(workloads, "Deployment") {
		replicaSetOwners, queryErr := client.Query(ctx, `count(kube_replicaset_owner)`)
		if queryErr != nil && ctx.Err() != nil {
			return resp
		}
		if queryErr != nil || firstValue(replicaSetOwners) == nil || *firstValue(replicaSetOwners) <= 0 {
			resp.Coverage.UnavailableKinds = appendUniqueSorted(resp.Coverage.UnavailableKinds, "Deployment")
			workloads = withoutScanKind(workloads, "Deployment")
			if queryErr != nil {
				replicaSetOwnersQueryFailed = true
				appendScanWarning(&resp, "deployment_owner_metrics_query_failed", queryErr.Error())
			}
		}
	}
	if len(workloads) == 0 {
		resp.Reason = "deployment_owner_metrics_missing"
		if replicaSetOwnersQueryFailed {
			resp.Reason = "deployment_owner_metrics_query_failed"
		}
		return resp
	}

	resp.Coverage.Batches = (len(workloads) + rightsizingScanBatchSize - 1) / rightsizingScanBatchSize
	publish(resp)
	for start := 0; start < len(workloads); start += rightsizingScanBatchSize {
		if err := ctx.Err(); err != nil {
			appendScanWarning(&resp, ReasonScanDeadlineExceeded, err.Error())
			break
		}
		end := min(start+rightsizingScanBatchSize, len(workloads))
		batch := workloads[start:end]
		batchStarted := time.Now()
		evidence := queryRightsizingScanBatch(ctx, client, batch, resp.ScannedAt)
		resp.Coverage.AttemptedBatches++
		log.Printf("[rightsizing] batch=%d workloads=%d duration=%s query_failures=%d", resp.Coverage.AttemptedBatches, len(batch), time.Since(batchStarted).Round(time.Millisecond), len(evidence.errors))
		if len(evidence.errors) == 0 {
			resp.Coverage.CompletedBatches++
		} else {
			// An interrupted batch is not backend failure evidence. Keep only
			// the batches that completed before cancellation or the scan deadline.
			if err := ctx.Err(); err != nil {
				appendScanWarning(&resp, ReasonScanDeadlineExceeded, err.Error())
				break
			}
			for key, queryErr := range evidence.errors {
				appendScanWarning(&resp, key+"_query_failed", queryErr.Error())
			}
		}
		for _, workload := range batch {
			out := buildScanWorkload(workload, evidence)
			resp.Workloads = append(resp.Workloads, out)
			resp.Coverage.WorkloadsEvaluated++
			if workloadHasData(out) {
				resp.Coverage.WorkloadsWithData++
			}
			if workloadHasUnavailableOOMEvidence(out) {
				appendScanWarning(&resp, ReasonOOMEvidenceUnavailable, "Restart history was incomplete for some memory recommendations.")
			}
		}
		publish(resp)
	}

	switch {
	case resp.Coverage.WorkloadsEvaluated == 0:
		resp.State = RightsizingScanUnavailable
		if resp.Reason == "" {
			resp.Reason = "scan_incomplete"
		}
	case len(resp.Warnings) > 0 || resp.Coverage.WorkloadsEvaluated < len(workloads) || len(resp.Coverage.RestrictedKinds) > 0 || len(resp.Coverage.UnavailableKinds) > 0 || len(resp.Coverage.PartiallyCachedKinds) > 0:
		resp.State = RightsizingScanPartial
		resp.Reason = ReasonSomeEvidenceUnavailable
	default:
		resp.State = RightsizingScanComplete
		if resp.Coverage.WorkloadsWithData == 0 {
			resp.Reason = "no_usage_samples"
		}
	}
	return resp
}

func queryRightsizingScanBatch(ctx context.Context, client rightsizingScanQuerier, batch []scanWorkload, now time.Time) scanBatchEvidence {
	queries := buildRightsizingScanQueries(batch)
	out := scanBatchEvidence{
		cpu: map[scanKey][]float64{}, memory: map[scanKey][]float64{}, throttle: map[scanKey][]float64{},
		restarts: map[scanKey]float64{}, terminations: map[scanKey]terminationEvidence{}, errors: map[string]error{},
	}
	// Prometheus aligns subquery steps to the epoch, so the per-workload path's
	// quantile_over_time(0.95, X[7d:5m]) always samples timestamps that are
	// multiples of the step. A range query instead walks outward from its own
	// start, so an unaligned start samples the series at different instants and
	// can read a different percentile — or miss a gauge's peak entirely —
	// for the same container the per-workload path just measured. Truncating to
	// the step puts both paths on one grid, which is what lets a caller drill
	// from a scan into scope=workload and get the same numbers.
	end := now.Truncate(rightsizingStep)
	start := end.Add(-rightsizingWindow)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 2)
	for _, key := range []string{"cpu", "memory", "throttle"} {
		key := key
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			out.errors[key] = ctx.Err()
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := client.QueryRange(ctx, queries[key], start, end, rightsizingStep)
			<-sem
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.errors[key] = err
				return
			}
			values := scanMatrixValues(result)
			switch key {
			case "cpu":
				out.cpu = values
			case "memory":
				out.memory = values
			case "throttle":
				out.throttle = values
			}
		}()
	}
	wg.Wait()
	for _, key := range []string{"restart_activity", "termination_history"} {
		key := key
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			out.errors[key] = ctx.Err()
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := client.Query(ctx, queries[key])
			<-sem
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.errors[key] = err
				return
			}
			if key == "restart_activity" {
				out.restarts = scanVectorValues(result)
			} else {
				out.terminations = scanTerminationEvidence(result)
			}
		}()
	}
	wg.Wait()
	return out
}

func buildRightsizingScanQueries(batch []scanWorkload) map[string]string {
	owner := rightsizingScanOwnerVector(batch)
	namespaces := batchNamespaces(batch)
	nsMatcher := labelRegexMatcher("namespace", namespaces)
	group := "namespace,workload_kind,workload,container"
	cpu := fmt.Sprintf(`max by (%s) (rate(container_cpu_usage_seconds_total{%s,container!="",container!="POD"}[5m]) * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	memory := fmt.Sprintf(`max by (%s) (container_memory_working_set_bytes{%s,container!="",container!="POD"} * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	throttled := fmt.Sprintf(`sum by (%s) (rate(container_cpu_cfs_throttled_periods_total{%s,container!="",container!="POD"}[5m]) * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	periods := fmt.Sprintf(`sum by (%s) (rate(container_cpu_cfs_periods_total{%s,container!="",container!="POD"}[5m]) * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	restarts := fmt.Sprintf(`max by (%s,pod) (kube_pod_container_status_restarts_total{%s,container!=""} * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	terminations := fmt.Sprintf(`max by (%s,pod,reason) (kube_pod_container_status_last_terminated_timestamp{%s,container!=""} * on (namespace,pod,container) group_left(reason) max by (namespace,pod,container,reason) (kube_pod_container_status_last_terminated_reason{%s,container!=""}) * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, nsMatcher, owner)
	return map[string]string{
		"cpu":                 cpu,
		"memory":              memory,
		"throttle":            fmt.Sprintf(`(%s) / (%s)`, throttled, periods),
		"restart_activity":    fmt.Sprintf(`sum by (%s) (increase((%s)[7d:5m]))`, group, restarts),
		"termination_history": fmt.Sprintf(`max by (%s,reason) (max_over_time((%s)[7d:5m])) > (time() - 604800)`, group, terminations),
	}
}

func rightsizingScanOwnerVector(batch []scanWorkload) string {
	byKindNamespace := map[string]map[string][]string{}
	for _, workload := range batch {
		if byKindNamespace[workload.kind] == nil {
			byKindNamespace[workload.kind] = map[string][]string{}
		}
		byKindNamespace[workload.kind][workload.namespace] = append(byKindNamespace[workload.kind][workload.namespace], workload.name)
	}
	var terms []string
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		namespaces := byKindNamespace[kind]
		var namespaceNames []string
		for namespace := range namespaces {
			namespaceNames = append(namespaceNames, namespace)
		}
		sort.Strings(namespaceNames)
		for _, namespace := range namespaceNames {
			names := namespaces[namespace]
			sort.Strings(names)
			ns := prom.SanitizeLabelValue(namespace)
			namePattern := exactRegex(names)
			if kind == "Deployment" {
				right := fmt.Sprintf(`label_replace(max by (namespace,replicaset,owner_name) (kube_replicaset_owner{namespace="%s",owner_kind="Deployment",owner_name=~"%s",owner_is_controller="true"}), "workload", "$1", "owner_name", "(.*)")`, ns, namePattern)
				joined := fmt.Sprintf(`label_replace(max by (namespace,pod,owner_name) (kube_pod_owner{namespace="%s",owner_kind="ReplicaSet",owner_is_controller="true"}), "replicaset", "$1", "owner_name", "(.*)") * on (namespace,replicaset) group_left(workload) (%s)`, ns, right)
				terms = append(terms, fmt.Sprintf(`label_replace((%s), "workload_kind", "Deployment", "workload", ".*")`, joined))
				continue
			}
			owner := fmt.Sprintf(`label_replace(max by (namespace,pod,owner_name) (kube_pod_owner{namespace="%s",owner_kind="%s",owner_name=~"%s",owner_is_controller="true"}), "workload", "$1", "owner_name", "(.*)")`, ns, kind, namePattern)
			terms = append(terms, fmt.Sprintf(`label_replace((%s), "workload_kind", "%s", "workload", ".*")`, owner, kind))
		}
	}
	return strings.Join(terms, " or ")
}

func labelRegexMatcher(label string, values []string) string {
	return fmt.Sprintf(`%s=~"%s"`, label, exactRegex(values))
}

func exactRegex(values []string) string {
	escaped := make([]string, 0, len(values))
	for _, value := range values {
		escaped = append(escaped, prom.EscapeRegexMeta(prom.SanitizeLabelValue(value)))
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

func batchNamespaces(batch []scanWorkload) []string {
	seen := map[string]bool{}
	for _, workload := range batch {
		seen[workload.namespace] = true
	}
	values := make([]string, 0, len(seen))
	for namespace := range seen {
		values = append(values, namespace)
	}
	sort.Strings(values)
	return values
}

func scanMatrixValues(result *prom.QueryResult) map[scanKey][]float64 {
	values := map[scanKey][]float64{}
	if result == nil {
		return values
	}
	for _, series := range result.Series {
		key, ok := scanSeriesKey(series.Labels)
		if !ok {
			continue
		}
		for _, point := range series.DataPoints {
			if !math.IsNaN(point.Value) && !math.IsInf(point.Value, 0) {
				values[key] = append(values[key], point.Value)
			}
		}
	}
	return values
}

func scanVectorValues(result *prom.QueryResult) map[scanKey]float64 {
	values := map[scanKey]float64{}
	if result == nil {
		return values
	}
	for _, series := range result.Series {
		key, ok := scanSeriesKey(series.Labels)
		if ok && len(series.DataPoints) > 0 && !math.IsNaN(series.DataPoints[0].Value) && !math.IsInf(series.DataPoints[0].Value, 0) {
			values[key] = series.DataPoints[0].Value
		}
	}
	return values
}

func scanTerminationEvidence(result *prom.QueryResult) map[scanKey]terminationEvidence {
	values := map[scanKey]terminationEvidence{}
	if result == nil {
		return values
	}
	for _, series := range result.Series {
		key, ok := scanSeriesKey(series.Labels)
		if !ok || len(series.DataPoints) == 0 || series.DataPoints[0].Value <= 0 {
			continue
		}
		evidence := values[key]
		evidence.Any = true
		evidence.OOM = evidence.OOM || series.Labels["reason"] == "OOMKilled"
		values[key] = evidence
	}
	return values
}

func scanSeriesKey(labels map[string]string) (scanKey, bool) {
	key := scanKey{namespace: labels["namespace"], kind: labels["workload_kind"], workload: labels["workload"], container: labels["container"]}
	return key, key.namespace != "" && key.kind != "" && key.workload != "" && key.container != ""
}

func buildScanWorkload(input scanWorkload, evidence scanBatchEvidence) RightsizingScanWorkload {
	out := RightsizingScanWorkload{Kind: input.kind, Namespace: input.namespace, Name: input.name, Replicas: input.replicas, ScaledToZero: input.workload.scaledToZero, ManagedBy: input.workload.managedBy, Rows: make([]RightsizingRow, 0, len(input.workload.containers)*2)}
	expected := int(rightsizingWindow/rightsizingStep) + 1
	for _, container := range input.workload.containers {
		key := scanKey{namespace: input.namespace, kind: input.kind, workload: input.name, container: container.name}
		for _, resourceName := range []string{"cpu", "memory"} {
			row := buildScanRow(container, resourceName, key, expected, input.workload, evidence)
			out.Rows = append(out.Rows, row)
		}
	}
	return out
}

func buildScanRow(container containerSpec, resourceName string, key scanKey, expected int, workload rightsizingWorkload, evidence scanBatchEvidence) RightsizingRow {
	row := RightsizingRow{
		Container: container.name, Resource: resourceName, Fit: FitInsufficientHistory, Confidence: ConfidenceLow,
		ExpectedSamples: expected, HPAManaged: workload.hpaManaged[resourceName], HPAEvidenceAvailable: workload.hpaAvailable,
		LiveInventoryUnavailable: workload.liveInventoryUnavailable,
	}
	var request, limit = container.cpuReq, container.cpuLim
	statistic := "P95"
	series := evidence.cpu[key]
	if resourceName == "memory" {
		request, limit = container.memReq, container.memLim
		statistic = "Max"
		series = evidence.memory[key]
		row.CurrentPodOOM = workload.currentPodOOM[container.name]
		if evidence.errors["restart_activity"] == nil && evidence.errors["termination_history"] == nil {
			restartActivity, restartAvailable := evidence.restarts[key]
			termination := evidence.terminations[key]
			row.WindowOOMEvidence = termination.OOM
			row.OOMEvidenceAvailable = termination.OOM || (restartAvailable && (restartActivity <= 0 || termination.Any))
		}
	}
	setCurrentQuantities(&row, request, limit, resourceName)
	if queryErr := evidence.errors[resourceName]; queryErr != nil {
		row.QueryError = RowUsageQueryFailed
		return row
	}
	if len(series) == 0 {
		return row
	}
	observed := percentile(series, 0.95)
	if resourceName == "memory" {
		observed = maxFinite(series)
	} else {
		peak := percentile(series, 0.99)
		row.Peak = &ObservedStatistic{Name: "P99", Value: peak, Formatted: formatObservedValue(peak, resourceName)}
		row.Bursty = isBurstyCPU(observed, peak)
	}
	row.Observed = &ObservedStatistic{Name: statistic, Value: observed, Formatted: formatObservedValue(observed, resourceName)}
	row.SampleCount = len(series)
	row.Coverage = math.Min(float64(row.SampleCount)/float64(expected), 1)
	row.Confidence = confidenceFor(row.SampleCount, row.Coverage, OwnerCoverageKSMHistory)
	if resourceName == "cpu" && evidence.errors["throttle"] == nil {
		if values := evidence.throttle[key]; len(values) > 0 {
			value := maxFinite(values)
			row.ThrottleAvailable = true
			row.ThrottleRatio = &value
		}
	}
	if row.SampleCount < rightsizingMinSamples {
		row.RecommendationReason = "insufficient_history"
		return row
	}
	classifyRightsizingFit(&row, observed, request, limit, resourceName)
	return row
}

func percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	if len(ordered) == 1 {
		return ordered[0]
	}
	position := quantile * float64(len(ordered)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return ordered[lower]
	}
	weight := position - float64(lower)
	return ordered[lower]*(1-weight) + ordered[upper]*weight
}

func maxFinite(values []float64) float64 {
	maximum := -math.MaxFloat64
	for _, value := range values {
		if !math.IsNaN(value) && !math.IsInf(value, 0) && value > maximum {
			maximum = value
		}
	}
	if maximum == -math.MaxFloat64 {
		return 0
	}
	return maximum
}

func workloadHasData(workload RightsizingScanWorkload) bool {
	for _, row := range workload.Rows {
		if row.Observed != nil {
			return true
		}
	}
	return false
}

func workloadHasUnavailableOOMEvidence(workload RightsizingScanWorkload) bool {
	for _, row := range workload.Rows {
		if row.Resource == "memory" && row.RecommendationReason == ReasonOOMEvidenceUnavailable {
			return true
		}
	}
	return false
}

// ReasonScanDeadlineExceeded is the warning a scan records when its context
// ended before every batch answered.
const ReasonScanDeadlineExceeded = "scan_deadline_exceeded"

// ReasonSomeEvidenceUnavailable is the reason a response carries when its rows
// are intact but a query behind them did not answer. A workload-scope answer
// borrows it so both scopes name the same situation the same way.
const ReasonSomeEvidenceUnavailable = "some_evidence_unavailable"

func appendScanWarning(resp *RightsizingScanResponse, code, message string) {
	for _, warning := range resp.Warnings {
		if warning.Code == code {
			return
		}
	}
	resp.Warnings = append(resp.Warnings, RightsizingScanWarning{Code: code, Message: boundWarningMessage(message)})
	sort.Slice(resp.Warnings, func(i, j int) bool { return resp.Warnings[i].Code < resp.Warnings[j].Code })
}

// maxWarningMessageBytes bounds one warning message. A Prometheus error can
// quote the whole failing query, so an unbounded scan warning runs to tens of
// kilobytes and crowds out the rows it is annotating.
const maxWarningMessageBytes = 400

// boundWarningMessage redacts the backend address from the message and caps
// what is left. Warnings reach MCP clients, so the address must go — it names
// the backend and can carry credentials. Consumers key on Code, never on
// Message, so the detail is diagnostic rather than load-bearing.
func boundWarningMessage(message string) string {
	message = prom.RedactURLs(message)
	if len(message) > maxWarningMessageBytes {
		message = message[:maxWarningMessageBytes] + "… (truncated)"
	}
	return message
}

func hasScanKind(workloads []scanWorkload, kind string) bool {
	for _, workload := range workloads {
		if workload.kind == kind {
			return true
		}
	}
	return false
}

func withoutScanKind(workloads []scanWorkload, kind string) []scanWorkload {
	out := workloads[:0]
	for _, workload := range workloads {
		if workload.kind != kind {
			out = append(out, workload)
		}
	}
	return out
}

// countDistinct counts kinds, not list entries: the coverage lists overlap — a
// kind readable in only some of the requested namespaces is both restricted and
// partially cached — so summing their lengths claims more limited kinds than
// exist, and "all kinds unavailable" would fire with one kind still readable.
func countDistinct(lists ...[]string) int {
	seen := make(map[string]struct{})
	for _, list := range lists {
		for _, value := range list {
			seen[value] = struct{}{}
		}
	}
	return len(seen)
}

func appendUniqueSorted(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	values = append(values, value)
	sort.Strings(values)
	return values
}

func sortScanWorkloads(workloads []scanWorkload) {
	sort.Slice(workloads, func(i, j int) bool {
		if workloads[i].namespace != workloads[j].namespace {
			return workloads[i].namespace < workloads[j].namespace
		}
		if workloads[i].kind != workloads[j].kind {
			return workloads[i].kind < workloads[j].kind
		}
		return workloads[i].name < workloads[j].name
	})
}

// snapshotScanWorkloads also returns each DaemonSet it skipped for matching no
// node, as namespace/name.
func snapshotScanWorkloads(ctx context.Context, cache *k8s.ResourceCache, scopes map[string][]string) ([]scanWorkload, []string, []string) {
	workloads := map[string]*scanWorkload{}
	var unavailable, skippedDaemonSets []string
	add := func(obj metav1.Object, kind string, replicas int, podSpec *corev1.PodSpec, scaledToZero bool) {
		key := workloadIdentity(kind, obj.GetNamespace(), obj.GetName())
		workloads[key] = &scanWorkload{kind: kind, namespace: obj.GetNamespace(), name: obj.GetName(), replicas: replicas, workload: rightsizingWorkload{
			containers: extractRuntimeContainers(podSpec), currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}, scaledToZero: scaledToZero,
			managedBy: detectWorkloadManager(obj),
		}}
	}

	if namespaces, ok := scopes["Deployment"]; ok {
		lister := cache.Deployments()
		if lister == nil {
			unavailable = append(unavailable, "Deployment")
		} else if items, err := listDeployments(lister, namespaces); err != nil {
			unavailable = append(unavailable, "Deployment")
		} else {
			for _, item := range items {
				replicas := specReplicas(item.Spec.Replicas)
				add(item, "Deployment", replicas, &item.Spec.Template.Spec, replicas == 0)
			}
		}
	}
	if namespaces, ok := scopes["StatefulSet"]; ok {
		lister := cache.StatefulSets()
		if lister == nil {
			unavailable = append(unavailable, "StatefulSet")
		} else if items, err := listStatefulSets(lister, namespaces); err != nil {
			unavailable = append(unavailable, "StatefulSet")
		} else {
			for _, item := range items {
				replicas := specReplicas(item.Spec.Replicas)
				add(item, "StatefulSet", replicas, &item.Spec.Template.Spec, replicas == 0)
			}
		}
	}
	if namespaces, ok := scopes["DaemonSet"]; ok {
		lister := cache.DaemonSets()
		if lister == nil {
			unavailable = append(unavailable, "DaemonSet")
		} else if items, err := listDaemonSets(lister, namespaces); err != nil {
			unavailable = append(unavailable, "DaemonSet")
		} else {
			for _, item := range items {
				// Skipped before batching, not filtered afterwards: they would
				// still spend query budget and turn the scan partial on rows
				// that can never carry evidence.
				if daemonSetMatchesNoNode(item) {
					skippedDaemonSets = append(skippedDaemonSets, item.Namespace+"/"+item.Name)
					continue
				}
				// A zero not yet re-observed is scanned, but still has no replica
				// count to weigh impact by.
				add(item, "DaemonSet", int(item.Status.DesiredNumberScheduled), &item.Spec.Template.Spec, item.Status.DesiredNumberScheduled == 0)
			}
		}
	}

	enrichScanHPA(ctx, cache, scopes, workloads)
	enrichScanCurrentOOM(ctx, cache, scopes, workloads)
	out := make([]scanWorkload, 0, len(workloads))
	for _, workload := range workloads {
		out = append(out, *workload)
	}
	sort.Strings(unavailable)
	return out, unavailable, skippedDaemonSets
}

// daemonSetMatchesNoNode trusts a zero desired count only once the controller
// has observed the current spec: a selector changed to match nodes keeps the
// old status until the controller reconciles it.
func daemonSetMatchesNoNode(ds *appsv1.DaemonSet) bool {
	return ds.Status.DesiredNumberScheduled == 0 && ds.Status.ObservedGeneration >= ds.Generation
}

func listDeployments(lister listersappsv1.DeploymentLister, namespaces []string) ([]*appsv1.Deployment, error) {
	if namespaces == nil {
		return lister.List(labels.Everything())
	}
	var out []*appsv1.Deployment
	for _, namespace := range namespaces {
		items, err := lister.Deployments(namespace).List(labels.Everything())
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func listStatefulSets(lister listersappsv1.StatefulSetLister, namespaces []string) ([]*appsv1.StatefulSet, error) {
	if namespaces == nil {
		return lister.List(labels.Everything())
	}
	var out []*appsv1.StatefulSet
	for _, namespace := range namespaces {
		items, err := lister.StatefulSets(namespace).List(labels.Everything())
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func listDaemonSets(lister listersappsv1.DaemonSetLister, namespaces []string) ([]*appsv1.DaemonSet, error) {
	if namespaces == nil {
		return lister.List(labels.Everything())
	}
	var out []*appsv1.DaemonSet
	for _, namespace := range namespaces {
		items, err := lister.DaemonSets(namespace).List(labels.Everything())
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func enrichScanHPA(ctx context.Context, cache *k8s.ResourceCache, scopes map[string][]string, workloads map[string]*scanWorkload) {
	lister := cache.HorizontalPodAutoscalers()
	if lister == nil {
		return
	}
	// Only this informer's sync state says whether the HPA inventory is
	// readable. The deferred phase as a whole reports false while any unrelated
	// kind is warming, and permanently once one fails, which would withhold a
	// correct recommendation for a reason that has nothing to do with
	// autoscaling.
	if !waitForInformerSynced(ctx, cache, string(k8score.HorizontalPodAutoscalers)) {
		return
	}
	namespaces := scanScopeNamespaces(scopes)
	var hpas []*autoscalingv2.HorizontalPodAutoscaler
	if namespaces == nil {
		var err error
		hpas, err = lister.List(labels.Everything())
		if err != nil {
			return
		}
		markScanHPAAvailable(cache, workloads, "")
	} else {
		for _, namespace := range namespaces {
			items, err := lister.HorizontalPodAutoscalers(namespace).List(labels.Everything())
			if err != nil {
				continue
			}
			hpas = append(hpas, items...)
			markScanHPAAvailable(cache, workloads, namespace)
		}
	}
	for _, hpa := range hpas {
		ref := hpa.Spec.ScaleTargetRef
		workload := workloads[workloadIdentity(ref.Kind, hpa.Namespace, ref.Name)]
		if workload == nil {
			continue
		}
		for _, metric := range hpa.Spec.Metrics {
			if metric.Type == autoscalingv2.ResourceMetricSourceType && metric.Resource != nil && metric.Resource.Target.AverageUtilization != nil {
				resourceName := string(metric.Resource.Name)
				if resourceName == "cpu" || resourceName == "memory" {
					workload.workload.hpaManaged[resourceName] = true
				}
			}
		}
	}
}

// markScanHPAAvailable records that the HPA inventory was actually readable for
// these workloads. A successful list says nothing about namespaces the informer
// does not watch, so coverage is checked per workload: a cluster-wide scan can
// span both kinds of namespace at once.
func markScanHPAAvailable(cache *k8s.ResourceCache, workloads map[string]*scanWorkload, namespace string) {
	for _, workload := range workloads {
		if namespace != "" && workload.namespace != namespace {
			continue
		}
		if !cache.KindCoversNamespace(string(k8score.HorizontalPodAutoscalers), workload.namespace) {
			continue
		}
		workload.workload.hpaAvailable = true
	}
}

// waitForInformerSynced reports whether an informer finished its initial sync,
// waiting out a warming cache within the same budget a single-workload read
// uses. A kind this cache never watched is not warming, and is left to the
// nil-lister check.
func waitForInformerSynced(ctx context.Context, cache *k8s.ResourceCache, key string) bool {
	deadline := time.Now().Add(warmingRetryBudget)
	for {
		synced, known := cache.InformerSynced(key)
		if synced || !known {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// markScanLiveInventoryUnavailable records that a live pod read did not happen
// for these workloads, so an absent OOM signal means nothing was asked rather
// than nothing was found. An empty kind marks every workload.
func markScanLiveInventoryUnavailable(workloads map[string]*scanWorkload, kind string) {
	for _, workload := range workloads {
		if kind == "" || strings.EqualFold(workload.kind, kind) {
			workload.workload.liveInventoryUnavailable = true
		}
	}
}

// scanNeedsReplicaSets reports whether any workload in the scan resolves its
// pods through a ReplicaSet. A StatefulSet or DaemonSet owns its pods directly,
// so a scan of only those must not wait on a deferred informer it never reads.
func scanNeedsReplicaSets(workloads map[string]*scanWorkload) bool {
	for _, workload := range workloads {
		if strings.EqualFold(workload.kind, "Deployment") {
			return true
		}
	}
	return false
}

func enrichScanCurrentOOM(ctx context.Context, cache *k8s.ResourceCache, scopes map[string][]string, workloads map[string]*scanWorkload) {
	// A warming informer hands out a non-nil lister that answers nothing, which
	// would read as a cluster with no OOM-killed pods anywhere. The scan pays
	// this wait once rather than per workload.
	if !waitForInformerSynced(ctx, cache, "pods") || cache.Pods() == nil {
		markScanLiveInventoryUnavailable(workloads, "")
		return
	}
	// Default false: a scan that needs no ReplicaSets never establishes them,
	// and must not then be treated as having read a lister it never touched.
	ownersReadable := false
	if scanNeedsReplicaSets(workloads) {
		ownersReadable = cache.ReplicaSets() != nil && waitForInformerSynced(ctx, cache, "replicasets")
	}
	if ctx.Err() != nil {
		// Nobody is waiting for this scan; do not walk the cluster for it.
		markScanLiveInventoryUnavailable(workloads, "")
		return
	}
	// A lister that is synced and non-nil still only answers for the namespaces
	// its informer watches. An empty result for a namespace outside that scope
	// is not "no OOM-killed pods" — it is no answer, and it has to be reported
	// per workload because the scan can span both kinds of namespace at once.
	for _, workload := range workloads {
		if !cache.KindCoversNamespace("pods", workload.namespace) {
			workload.workload.liveInventoryUnavailable = true
			continue
		}
		if strings.EqualFold(workload.kind, "Deployment") &&
			(!ownersReadable || !cache.KindCoversNamespace("replicasets", workload.namespace)) {
			workload.workload.liveInventoryUnavailable = true
		}
	}

	namespaces := scanScopeNamespaces(scopes)
	replicaSetOwners, ownersListed := scanReplicaSetOwners(ctx, cache, namespaces, ownersReadable)
	pods, podsListed := scanPods(cache, namespaces)
	if !podsListed {
		markScanLiveInventoryUnavailable(workloads, "")
		return
	}
	if !ownersListed {
		markScanLiveInventoryUnavailable(workloads, "Deployment")
	}
	for _, pod := range pods {
		owner := metav1.GetControllerOf(pod)
		if owner == nil {
			continue
		}
		kind, name := owner.Kind, owner.Name
		if owner.Kind == "ReplicaSet" {
			kind, name = "Deployment", replicaSetOwners[pod.Namespace+"\x00"+owner.Name]
		}
		workload := workloads[workloadIdentity(kind, pod.Namespace, name)]
		if workload == nil {
			continue
		}
		collectCurrentPodOOM(workload.workload.currentPodOOM, pod.Status.ContainerStatuses)
		collectCurrentPodOOM(workload.workload.currentPodOOM, pod.Status.InitContainerStatuses)
	}
}

// scanReplicaSetOwners maps each ReplicaSet to its Deployment. It reports false
// if any list failed, because a partial map silently drops the pods it could
// not resolve rather than failing loudly.
func scanReplicaSetOwners(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, readable bool) (map[string]string, bool) {
	owners := map[string]string{}
	if !readable || cache.ReplicaSets() == nil {
		return owners, false
	}
	var replicaSets []*appsv1.ReplicaSet
	if namespaces == nil {
		list, err := cache.ReplicaSets().List(labels.Everything())
		if err != nil {
			return owners, false
		}
		replicaSets = list
	} else {
		for _, namespace := range namespaces {
			items, err := cache.ReplicaSets().ReplicaSets(namespace).List(labels.Everything())
			if err != nil {
				return owners, false
			}
			replicaSets = append(replicaSets, items...)
		}
	}
	for _, replicaSet := range replicaSets {
		if owner := metav1.GetControllerOf(replicaSet); owner != nil && owner.Kind == "Deployment" {
			owners[replicaSet.Namespace+"\x00"+replicaSet.Name] = owner.Name
		}
	}
	return owners, true
}

// scanPods reports false if any list failed: a short pod list reads as pods
// without OOM history rather than pods that were never examined.
func scanPods(cache *k8s.ResourceCache, namespaces []string) ([]*corev1.Pod, bool) {
	if namespaces == nil {
		pods, err := cache.Pods().List(labels.Everything())
		if err != nil {
			return nil, false
		}
		return pods, true
	}
	var pods []*corev1.Pod
	for _, namespace := range namespaces {
		items, err := cache.Pods().Pods(namespace).List(labels.Everything())
		if err != nil {
			return nil, false
		}
		pods = append(pods, items...)
	}
	return pods, true
}

func scanScopeNamespaces(scopes map[string][]string) []string {
	seen := map[string]bool{}
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		namespaces, ok := scopes[kind]
		if !ok {
			continue
		}
		if namespaces == nil {
			return nil
		}
		for _, namespace := range namespaces {
			seen[namespace] = true
		}
	}
	out := make([]string, 0, len(seen))
	for namespace := range seen {
		out = append(out, namespace)
	}
	sort.Strings(out)
	return out
}

func workloadIdentity(kind, namespace, name string) string {
	return strings.ToLower(kind) + "\x00" + namespace + "\x00" + name
}
