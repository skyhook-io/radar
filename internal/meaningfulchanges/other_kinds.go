package meaningfulchanges

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var otherKindsDisabled atomic.Bool

// SetOtherKinds turns the default feed's other-kinds read (see
// RecentOtherKinds) on or off; it is on unless the operator turns it off.
func SetOtherKinds(enabled bool) { otherKindsDisabled.Store(!enabled) }

// OtherKindsEnabled reports whether callers should read RecentOtherKinds.
func OtherKindsEnabled() bool { return !otherKindsDisabled.Load() }

// churnKinds are custom kinds controllers create, delete or rewrite as
// routine bookkeeping without owner references, so their rows say nothing
// about an incident. The other-kinds read leaves them out before its limit;
// a kind= query still lists them.
var churnKinds = []resourceid.GroupKind{
	// Cilium allocates identities and endpoints as pods come and go.
	{Group: "cilium.io", Kind: "CiliumIdentity"},
	{Group: "cilium.io", Kind: "CiliumEndpoint"},
	{Group: "cilium.io", Kind: "CiliumEndpointSlice"},
	// Calico IPAM allocations, through its CRDs or its API server.
	{Group: "crd.projectcalico.org", Kind: "IPAMBlock"},
	{Group: "crd.projectcalico.org", Kind: "IPAMHandle"},
	{Group: "crd.projectcalico.org", Kind: "BlockAffinity"},
	{Group: "projectcalico.org", Kind: "IPAMBlock"},
	{Group: "projectcalico.org", Kind: "BlockAffinity"},
	// Policy engine reports and requests.
	{Group: "wgpolicyk8s.io", Kind: "PolicyReport"},
	{Group: "wgpolicyk8s.io", Kind: "ClusterPolicyReport"},
	{Group: "reports.kyverno.io", Kind: "EphemeralReport"},
	{Group: "reports.kyverno.io", Kind: "ClusterEphemeralReport"},
	{Group: "kyverno.io", Kind: "UpdateRequest"},
	// cert-manager issues these per renewal; the Certificate is the config.
	{Group: "cert-manager.io", Kind: "CertificateRequest"},
	{Group: "acme.cert-manager.io", Kind: "Order"},
	{Group: "acme.cert-manager.io", Kind: "Challenge"},
	// Provisioned per snapshot.
	{Group: "snapshot.storage.k8s.io", Kind: "VolumeSnapshotContent"},
	// Chaos Mesh's per-pod working state for a chaos experiment.
	{Group: "chaos-mesh.org", Kind: "PodNetworkChaos"},
	{Group: "chaos-mesh.org", Kind: "PodIOChaos"},
	{Group: "chaos-mesh.org", Kind: "PodHttpChaos"},
}

// controllerTaints are taints controllers add and remove on their own:
// those mirroring state the Node row records elsewhere (its conditions,
// spec.unschedulable, cloud-provider and Karpenter registration, and any
// "<agent>/agent-not-ready" startup taint a node agent such as Cilium
// removes once it runs) and the autoscaler and Karpenter removal taints, which Karpenter re-applies every few minutes
// while a consolidation keeps failing. Rows that change only these stay in
// history and kind=Node but not in the default feed; a Node actually removed
// shows as its delete.
var controllerTaints = map[string]bool{
	"node.kubernetes.io/not-ready":                   true,
	"node.kubernetes.io/unreachable":                 true,
	"node.kubernetes.io/memory-pressure":             true,
	"node.kubernetes.io/disk-pressure":               true,
	"node.kubernetes.io/pid-pressure":                true,
	"node.kubernetes.io/network-unavailable":         true,
	"node.kubernetes.io/unschedulable":               true,
	"node.cloudprovider.kubernetes.io/uninitialized": true,
	"karpenter.sh/disrupted":                         true,
	"karpenter.sh/unregistered":                      true,
	"ToBeDeletedByClusterAutoscaler":                 true,
	"DeletionCandidateOfClusterAutoscaler":           true,
}

// OtherKindsResult is RecentOtherKinds' answer.
type OtherKindsResult struct {
	Changes        []issuesapi.RecentChange
	FetchSaturated bool
}

// RecentOtherKinds returns, ranked and with no limit, the rows of kinds outside
// the curated ones: custom resources and Nodes. ComposeFeed picks from them.
// They are read and ranked apart from Recent's, so they cannot change which
// curated rows a query selects. Authorization fails closed without an error, so
// a context that ends during ranking is returned as one, with the rows that
// passed: dropped rows must not read as rows that never existed.
func RecentOtherKinds(ctx context.Context, q Query) (OtherKindsResult, error) {
	store := timeline.GetStore()
	if store == nil {
		return OtherKindsResult{}, timeline.StoreMissingError()
	}
	q = normalizeQuery(q)
	events, saturated, err := otherKindsCandidates(ctx, store, q, FetchBound)
	if err != nil {
		return OtherKindsResult{}, err
	}
	q.Limit = 0
	changes, _ := rankedChanges(ctx, coalesceRecreatePairs(dedupeEvents(events)), q)
	return OtherKindsResult{Changes: changes, FetchSaturated: saturated}, ctx.Err()
}

// otherKindsCandidates reads the default feed's rows outside the curated
// kinds: desired-state edits, creates and deletes of unowned custom resources
// outside churnKinds, and of Nodes. Status-only updates are dropped in the
// store, before its limit, so a controller's status writes cannot use up the
// read; a burst of desired-state edits still can, which saturation reports.
func otherKindsCandidates(ctx context.Context, store timeline.EventStore, q Query, bound int) ([]timeline.TimelineEvent, bool, error) {
	base := timeline.QueryOptions{
		Namespaces:          q.Namespaces,
		Since:               q.start,
		Sources:             []timeline.EventSource{timeline.SourceInformer},
		ClusterContext:      k8s.ActiveClusterContext(),
		DesiredStateUpdates: true,
		Limit:               bound + 1,
	}
	custom := base
	custom.RequireAPIVersion = true
	custom.ExcludeAPIGroups = k8score.BuiltInAPIGroups()
	custom.ExcludeGroupKinds = append(trackedCustomGroupKinds(), churnKinds...)
	reads := []timeline.QueryOptions{custom, custom}
	reads[0].EventTypes, reads[1].EventTypes = updateEventTypes, lifecycleEventTypes
	// A Node's owner (Karpenter's NodeClaim) manages the machine; the Node's
	// taints and labels are its own configuration. Nodes are cluster-scoped.
	if len(q.Namespaces) == 0 || slices.Contains(q.Namespaces, "") {
		nodes := base
		nodes.Kinds, nodes.APIGroups, nodes.IncludeManaged = []string{"Node"}, []string{""}, true
		reads = append(reads, nodes, nodes)
		reads[len(reads)-2].EventTypes, reads[len(reads)-1].EventTypes = updateEventTypes, lifecycleEventTypes
	}
	var events []timeline.TimelineEvent
	saturated := false
	for _, opts := range reads {
		got, err := store.Query(ctx, opts)
		if err != nil {
			return nil, false, err
		}
		if len(got) > bound {
			saturated = true
			got = got[:bound]
		}
		for _, e := range got {
			if isNodeEvent(e) && e.EventType == timeline.EventTypeUpdate && onlyControllerNodeChanges(e) {
				continue
			}
			events = append(events, e)
		}
	}
	return events, saturated, nil
}

// trackedCustomGroupKinds are the custom kinds the curated reads already
// list, by the group they are recorded from.
func trackedCustomGroupKinds() []resourceid.GroupKind {
	var out []resourceid.GroupKind
	for kind, group := range trackedKindGroups {
		if !k8score.IsBuiltInAPIGroup(group) {
			out = append(out, resourceid.GroupKind{Group: group, Kind: kind})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group+"/"+out[i].Kind < out[j].Group+"/"+out[j].Kind })
	return out
}

// controllerNodeLabels are labels Karpenter and the cluster autoscaler set
// on a Node they are removing.
var controllerNodeLabels = map[string]bool{
	"node.kubernetes.io/exclude-from-external-load-balancers": true,
}

func controllerTaint(key string) bool {
	return controllerTaints[key] || strings.HasSuffix(key, "/agent-not-ready")
}

// onlyControllerNodeChanges reports a Node row whose every desired-state
// field adds, removes or re-values controllerTaints or controllerNodeLabels.
// A field it can't read as taint keys counts as a change, rather than hide
// one.
func onlyControllerNodeChanges(e timeline.TimelineEvent) bool {
	if e.Diff == nil {
		return false
	}
	controllerFields := 0
	for _, f := range e.Diff.Fields {
		if !timeline.IsDesiredStatePath(f.Path) {
			continue
		}
		if key, ok := strings.CutPrefix(f.Path, "metadata.labels["); ok {
			if !controllerNodeLabels[strings.TrimSuffix(key, "]")] {
				return false
			}
			controllerFields++
			continue
		}
		if key, ok := strings.CutPrefix(f.Path, "spec.taints["); ok {
			if !controllerTaint(strings.TrimSuffix(key, "]")) {
				return false
			}
			controllerFields++
			continue
		}
		if f.Path != "spec.taints" {
			return false
		}
		oldKeys, newKeys := stringList(f.OldValue), stringList(f.NewValue)
		changed := append(subtract(oldKeys, newKeys), subtract(newKeys, oldKeys)...)
		if len(changed) == 0 {
			return false
		}
		for _, key := range changed {
			if !controllerTaint(key) {
				return false
			}
		}
		controllerFields++
	}
	return controllerFields > 0
}

const (
	// OtherKindsReserve is how many page slots a call that doesn't set a limit
	// reserves for other kinds. Its default page is larger by the same number.
	OtherKindsReserve = 5
	// otherKindsReservedPerKind is the most reserved slots one kind can hold.
	otherKindsReservedPerKind = 2
)

// KindCount names a kind and how many of its rows a selection left out.
type KindCount struct {
	Kind  string
	Group string
	Count int
}

// FeedSelection is ComposeFeed's result.
type FeedSelection struct {
	Changes []issuesapi.RecentChange
	// CuratedShown is how many of Changes came from the curated rows.
	CuratedShown int
	// Reserved is how many of Changes took slots reserved from curated rows.
	Reserved int
	// LeftOut lists the other kinds with rows that didn't fit.
	LeftOut []KindCount
}

// ComposeFeed builds the default feed's page. Curated rows are cut by rank
// alone, and rows of other kinds take up to reserve of the page, then any
// slots curated rows leave empty. Reserved slots go by rank, kind by kind,
// because they cost a curated row. Empty slots cost nothing, so they go kind by
// kind whatever the rank: a controller rewriting one kind keeps a row, which
// may be the lead, without burying the other kinds.
func ComposeFeed(curated, other []issuesapi.RecentChange, limit, reserve int) FeedSelection {
	RankAndCap(&curated, 0)
	RankAndCap(&other, 0)
	reserved, rest := reserveOtherRows(other, reserve)
	shown := min(len(curated), limit-len(reserved))
	spare := byKindInTurn(rest)
	taken := min(len(spare), limit-shown-len(reserved))

	page := slices.Concat(curated[:shown], reserved, spare[:taken])
	RankAndCap(&page, 0)

	left := map[resourceid.GroupKind]int{}
	for _, c := range spare[taken:] {
		left[changeGroupKind(c)]++
	}
	sel := FeedSelection{Changes: page, CuratedShown: shown, Reserved: len(reserved)}
	for key, n := range left {
		sel.LeftOut = append(sel.LeftOut, KindCount{Kind: key.Kind, Group: key.Group, Count: n})
	}
	sortKindCounts(sel.LeftOut)
	return sel
}

// reserveOtherRows picks up to reserve of rows, which must be ranked: every
// row of one score before the next, and within a score one row per kind in
// turn, so an edit of a represented kind beats a create of a new one.
func reserveOtherRows(rows []issuesapi.RecentChange, reserve int) (picked, rest []issuesapi.RecentChange) {
	perKind := map[resourceid.GroupKind]int{}
	for start := 0; start < len(rows); {
		end := start
		for end < len(rows) && score(rows[end]) == score(rows[start]) {
			end++
		}
		for _, c := range byKindInTurn(rows[start:end]) {
			key := changeGroupKind(c)
			if len(picked) < reserve && perKind[key] < otherKindsReservedPerKind {
				perKind[key]++
				picked = append(picked, c)
			} else {
				rest = append(rest, c)
			}
		}
		start = end
	}
	return picked, rest
}

// byKindInTurn orders rows one per kind in turn, each kind's best row first
// and kinds by their best row.
func byKindInTurn(rows []issuesapi.RecentChange) []issuesapi.RecentChange {
	rows = slices.Clone(rows)
	RankAndCap(&rows, 0)
	byKind := map[resourceid.GroupKind][]issuesapi.RecentChange{}
	var kinds []resourceid.GroupKind
	for _, c := range rows {
		key := changeGroupKind(c)
		if byKind[key] == nil {
			kinds = append(kinds, key)
		}
		byKind[key] = append(byKind[key], c)
	}
	order := make([]issuesapi.RecentChange, 0, len(rows))
	for turn := 0; len(order) < len(rows); turn++ {
		for _, key := range kinds {
			if turn < len(byKind[key]) {
				order = append(order, byKind[key][turn])
			}
		}
	}
	return order
}

func changeGroupKind(c issuesapi.RecentChange) resourceid.GroupKind {
	return resourceid.GroupKind{Group: resourceid.GroupFromAPIVersion(c.APIVersion), Kind: c.Kind}
}

func sortKindCounts(list []KindCount) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		return list[i].Kind+"."+list[i].Group < list[j].Kind+"."+list[j].Group
	})
}

// FeedSelectionGuidance is the narrowHint text naming the other kinds
// ComposeFeed left rows out of, or "".
func FeedSelectionGuidance(sel FeedSelection) string {
	if len(sel.LeftOut) == 0 {
		return ""
	}
	pass := "kind=<Kind>"
	if len(sel.LeftOut) == 1 {
		pass = "kind=" + sel.LeftOut[0].Kind
	}
	return fmt.Sprintf("Left out: %s. Pass %s to list them.", kindCountList(sel.LeftOut), pass)
}

func kindCountList(list []KindCount) string {
	const named = 5
	parts := make([]string, 0, named+1)
	for _, k := range list[:min(len(list), named)] {
		noun := "changes"
		if k.Count == 1 {
			noun = "change"
		}
		parts = append(parts, fmt.Sprintf("%d %s %s", k.Count, k.Kind, noun))
	}
	if len(list) > named {
		parts = append(parts, fmt.Sprintf("%d more kinds", len(list)-named))
	}
	return strings.Join(parts, ", ")
}
