package prometheus

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// A source is a scrape target, including its remote-write identity when present.
// Matching a job alone could let one cluster's exporter vouch for another's.
func podInventorySourceLabels(ctx context.Context) []string {
	return append([]string{"job", "instance", "endpoint", "service", "prometheus", "prometheus_replica", "replica", "__replica__"}, partitionLabelsFor(ctx)...)
}

// Only a fully verified result supersedes the ordinary view decision. A
// mixed-label store can still provide its original unverified local view.
func (c *Client) verifiedRightsizingOwnerScope(ctx context.Context, req ViewScopeRequest, at time.Time, namespace string, anchors []prom.WorkloadPodIdentity, cache *k8s.ResourceCache) (ViewScope, bool) {
	if cache == nil || cache.Nodes() == nil {
		return ViewScope{}, false
	}
	nodes, err := cache.Nodes().List(labels.Everything())
	if err != nil {
		return ViewScope{}, false
	}
	probeCtx, cancel := context.WithTimeout(ctx, historicalPartitionProbeBudget)
	defer cancel()
	partition, err := rightsizingOwnerPartition(probeCtx, c, namespace, anchors, nodes, time.Now())
	if err != nil || len(partition) == 0 {
		return ViewScope{}, false
	}
	// A retired scrape target can contribute owner history under the same
	// cluster label without appearing in today's inventory.
	ok, err := rightsizingSourcesMatchNodes(probeCtx, c, at, req.Span, partition, nil, nodes)
	if err != nil || !ok {
		return ViewScope{}, false
	}
	scope, err := decideViewScope(probeCtx, c, req, at, partition, false)
	return scope, err == nil && scope.State == ViewScopeVerified && !scope.Withheld
}

func rightsizingOwnerPartition(ctx context.Context, q instantQuerier, namespace string, anchors []prom.WorkloadPodIdentity, nodes []*corev1.Node, at time.Time) (map[string]string, error) {
	if len(nodes) == 0 || len(nodes) > 1024 {
		return nil, nil
	}
	names := make([]string, len(anchors))
	for i, pod := range anchors {
		names[i] = regexp.QuoteMeta(pod.Name)
	}
	matchers := []viewMatcher{{label: "namespace", op: "=", value: namespace}, {label: "pod", op: "=~", value: strings.Join(names, "|")}}
	sourceLabels := podInventorySourceLabels(ctx)
	group := "namespace,pod,uid," + strings.Join(sourceLabels, ",")
	probes, err := planViewProbes([]viewProbePart{
		{tag: viewScopeMetricLabel, metric: "kube_pod_info", head: "topk(257,count by (" + group + ") (", tail: "))", matchers: matchers},
		{tag: viewScopeMetricLabel, metric: metricPodOwner, head: "topk(257,count by (" + group + ",owner_kind,owner_name) (", tail: "))",
			matchers: append(matchers, parseViewMatchers(`owner_is_controller="true",owner_kind!="",owner_name!=""`)...)},
	}, prom.MaxWorkloadQueryBytes)
	if err != nil {
		return nil, err
	}
	var rows []prom.Series
	counts := map[string]int{}
	for _, probe := range probes {
		result, err := requireComplete(q.QueryAtPost(ctx, probe.query, at))
		if err != nil {
			return nil, err
		}
		if result == nil || len(result.Annotations.Infos) != 0 || result.ResultType != "vector" || len(rows)+len(result.Series) > 512 {
			return nil, nil
		}
		for _, row := range result.Series {
			family := row.Labels[viewScopeMetricLabel]
			counts[family]++
			if counts[family] > 256 {
				return nil, nil
			}
		}
		rows = append(rows, result.Series...)
	}

	type subject struct{ namespace, pod, partition string }
	type owner struct {
		subject
		kind, name string
	}
	ownerOf := func(l map[string]string) owner {
		return owner{subject{l["namespace"], l["pod"], identityKey(l, partitionLabelsFor(ctx))}, l["owner_kind"], l["owner_name"]}
	}
	knownOwners := map[owner]bool{}
	knownPods := map[subject]bool{}
	var strong []prom.Series
	for _, row := range rows {
		l := row.Labels
		if l["namespace"] != namespace || (l[viewScopeMetricLabel] != metricPodOwner && l[viewScopeMetricLabel] != "kube_pod_info") {
			return nil, nil
		}
		if l["uid"] == "" {
			continue
		}
		// A UID that appeared on Pod info since the primary check must be
		// handled by that check on retry, including any contradictions.
		if l[viewScopeMetricLabel] != metricPodOwner || l["owner_name"] == "" || l["owner_kind"] == "" {
			return nil, nil
		}
		o := ownerOf(l)
		knownOwners[o], knownPods[o.subject] = true, true
		strong = append(strong, row)
	}
	partition, _ := identifyPartition(ctx, anchors, strong)
	if len(partition) == 0 {
		return nil, nil
	}
	selected := labels.SelectorFromSet(partition)
	sources := map[string]bool{}
	controllers := map[subject]owner{}
	for _, row := range rows {
		l := row.Labels
		if !selected.Matches(labels.Set(l)) {
			continue
		}
		o := ownerOf(l)
		if l["uid"] != "" {
			if prior, ok := controllers[o.subject]; ok && prior != o {
				return nil, nil
			}
			controllers[o.subject] = o
			continue
		}
		if l["job"] == "" || l["instance"] == "" || !knownPods[o.subject] ||
			(l[viewScopeMetricLabel] == metricPodOwner && !knownOwners[o]) {
			return nil, nil
		}
		sources[identityKey(l, sourceLabels)] = true
	}
	if len(sources) == 0 {
		return nil, nil
	}
	ok, err := rightsizingSourcesMatchNodes(ctx, q, at, 0, partition, sources, nodes)
	if !ok || err != nil {
		return nil, err
	}
	return partition, nil
}

func rightsizingSourcesMatchNodes(ctx context.Context, q instantQuerier, at time.Time, span time.Duration, partition map[string]string, sources map[string]bool, nodes []*corev1.Node) (bool, error) {
	matchers, err := (prom.WorkloadMetricsScope{ClusterLabels: partition}).Matchers()
	if err != nil {
		return false, err
	}
	sourceLabels := podInventorySourceLabels(ctx)
	group := strings.Join(sourceLabels, ",")
	observe := func(selector string) string {
		if span > 0 {
			return "present_over_time(" + selector + "[" + promSeconds(span) + "])"
		}
		return selector
	}
	// Check all UID-less reporters for this partition, even one whose Pods
	// were absent from the bounded anchor sample.
	info := observe(`kube_pod_info{` + matchers + `,uid=""}`)
	owner := observe(`kube_pod_owner{` + matchers + `,uid="",owner_is_controller="true"}`)
	reporters := `topk(65,count by (` + group + `) (` + info + ` or ` + owner + `))`
	identities := `topk(1025,count by (` + group + `,node,system_uuid,provider_id) (` + observe(`kube_node_info{`+matchers+`}`) + `))`
	query := viewTagged(viewScopeMetricLabel, "sources", reporters) + " or " + viewTagged(viewScopeMetricLabel, "nodes", identities)
	if len(query) > prom.MaxWorkloadQueryBytes {
		return false, prom.ErrWorkloadQueryTooLarge
	}
	result, err := requireComplete(q.QueryAtPost(ctx, query, at))
	if err != nil {
		return false, err
	}
	if result == nil || len(result.Annotations.Infos) != 0 || result.ResultType != "vector" || len(result.Series) > 1088 {
		return false, nil
	}
	apiNodes := make(map[string]*corev1.Node, len(nodes))
	providers := map[string]bool{}
	for _, node := range nodes {
		if node == nil || node.Spec.ProviderID == "" || providers[node.Spec.ProviderID] {
			return false, nil
		}
		apiNodes[node.Name] = node
		providers[node.Spec.ProviderID] = true
	}
	selected := labels.SelectorFromSet(partition)
	var nodeRows []prom.Series
	required := map[string]bool{}
	sourceRows := 0
	for _, row := range result.Series {
		l := row.Labels
		if !selected.Matches(labels.Set(l)) {
			return false, nil
		}
		switch l[viewScopeMetricLabel] {
		case "sources":
			sourceRows++
			if l["job"] == "" || l["instance"] == "" {
				return false, nil
			}
			required[identityKey(l, sourceLabels)] = true
		case "nodes":
			nodeRows = append(nodeRows, row)
		default:
			return false, nil
		}
	}
	if sourceRows == 0 || sourceRows > 64 || len(nodeRows) > 1024 {
		return false, nil
	}
	for source := range sources {
		if !required[source] {
			return false, nil
		}
	}
	seen := map[string]map[string]bool{}
	for _, row := range nodeRows {
		l := row.Labels
		source := identityKey(l, sourceLabels)
		if !required[source] && l["provider_id"] == "" && l["system_uuid"] == "" {
			continue
		}
		node := apiNodes[l["node"]]
		// Nodes can retire during a history window. They cannot establish
		// identity. A reused live node name with contrary identity still fails.
		if span > 0 && node == nil {
			if providers[l["provider_id"]] {
				return false, nil
			}
			continue
		}
		if node == nil || (required[source] || l["provider_id"] != "") && l["provider_id"] != node.Spec.ProviderID || l["system_uuid"] != "" && !strings.EqualFold(l["system_uuid"], node.Status.NodeInfo.SystemUUID) {
			return false, nil
		}
		if seen[source] == nil {
			seen[source] = map[string]bool{}
		}
		seen[source][node.Name] = true
	}
	for source := range required {
		if len(seen[source]) == 0 || (span == 0 && len(seen[source]) != len(apiNodes)) {
			return false, nil
		}
	}
	if span > 0 {
		return rightsizingReportersContinuous(ctx, q, at, span, matchers, slices.Sorted(maps.Keys(providers)))
	}
	return true, nil
}

// A reused scrape address must not connect disjoint Node fleets across time.
// Every rightsizing interval with UID-less inventory needs a local Node witness
// on that reporter. One overlap with current hardware allows normal Node churn.
func rightsizingReportersContinuous(ctx context.Context, q instantQuerier, at time.Time, span time.Duration, matchers string, providers []string) (bool, error) {
	group := strings.Join(podInventorySourceLabels(ctx), ",")
	step := promSeconds(rightsizingStep)
	// Align the subquery outwards to include both partial edge intervals.
	end := at.Truncate(rightsizingStep)
	if end.Before(at) {
		end = end.Add(rightsizingStep)
	}
	window := "[" + promSeconds(span+rightsizingStep) + ":" + step + "]"
	observed := func(metric, extra string) string {
		return "present_over_time(" + metric + "{" + matchers + "," + extra + "}[" + step + "])"
	}
	bySource := func(expr string) string { return "count by (" + group + ") (" + expr + ")" }
	nodes := func(ids []string) string { return observed("kube_node_info", labelRegexMatcher("provider_id", ids)) }
	query := func(expr string) (*prom.QueryResult, error) {
		if len(expr) > prom.MaxWorkloadQueryBytes {
			return nil, prom.ErrWorkloadQueryTooLarge
		}
		r, err := requireComplete(q.QueryAtPost(ctx, expr, end))
		if err != nil || r == nil || r.ResultType != "vector" || len(r.Annotations.Infos) != 0 {
			return nil, err
		}
		return r, nil
	}
	allNodes := "count by (" + group + ",provider_id) (" + observed("kube_node_info", `provider_id!=""`) + ")"
	overlap := allNodes + " and on (" + group + ") " + bySource(nodes(providers))
	witnesses, err := query("topk(1025,count by (provider_id) (max_over_time((" + overlap + ")" + window + ")))")
	if err != nil || witnesses == nil || len(witnesses.Series) == 0 || len(witnesses.Series) > 1024 {
		return false, err
	}
	var ids []string
	for _, row := range witnesses.Series {
		id := row.Labels["provider_id"]
		if id == "" {
			return false, nil
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	inventory := bySource(observed("kube_pod_info", `uid=""`) + " or " + observed(metricPodOwner, `uid="",owner_is_controller="true"`))
	missing, err := query("topk(1,max_over_time((" + inventory + " unless " + bySource(nodes(ids)) + ")" + window + "))")
	return err == nil && missing != nil && len(missing.Series) == 0, err
}
