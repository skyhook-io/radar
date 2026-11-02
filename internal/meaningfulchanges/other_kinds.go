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
// Query.OtherKinds) on or off; it is on unless the operator turns it off.
func SetOtherKinds(enabled bool) { otherKindsDisabled.Store(!enabled) }

// OtherKindsEnabled reports whether callers should set Query.OtherKinds.
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

// readinessTaints mirror state the Node row records elsewhere: its
// conditions, spec.unschedulable, or cloud-provider initialization. Rows that
// change only these taints stay in history but not in the default feed.
// Autoscaler and Karpenter removal taints are not here: they mark a removal
// in progress.
var readinessTaints = map[string]bool{
	"node.kubernetes.io/not-ready":                   true,
	"node.kubernetes.io/unreachable":                 true,
	"node.kubernetes.io/memory-pressure":             true,
	"node.kubernetes.io/disk-pressure":               true,
	"node.kubernetes.io/pid-pressure":                true,
	"node.kubernetes.io/network-unavailable":         true,
	"node.kubernetes.io/unschedulable":               true,
	"node.cloudprovider.kubernetes.io/uninitialized": true,
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
		Until:               q.end,
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
		reads = append(reads, nodes)
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
			if isNodeEvent(e) && e.EventType == timeline.EventTypeUpdate && onlyReadinessTaintChanges(e) {
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

// onlyReadinessTaintChanges reports a Node row whose every desired-state
// field adds, removes or re-values readinessTaints. A field it can't read as
// taint keys counts as a change, rather than hide one.
func onlyReadinessTaintChanges(e timeline.TimelineEvent) bool {
	if e.Diff == nil {
		return false
	}
	taintFields := 0
	for _, f := range e.Diff.Fields {
		if !timeline.IsDesiredStatePath(f.Path) {
			continue
		}
		if key, ok := strings.CutPrefix(f.Path, "spec.taints["); ok {
			if !readinessTaints[strings.TrimSuffix(key, "]")] {
				return false
			}
			taintFields++
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
			if !readinessTaints[key] {
				return false
			}
		}
		taintFields++
	}
	return taintFields > 0
}

const (
	// perKindShown is how many rows of one kind the default feed shows
	// while other kinds' rows are left out to make room.
	perKindShown = 3
	// Churn guard: a kind outside the curated set with at least
	// churnMinRows rows in the window, at least churnRowsPerObject per
	// object on average, is controller churn: a controller rewriting the
	// same objects. A bulk apply of many objects, once each, is not.
	churnMinRows       = 50
	churnRowsPerObject = 5
)

// KindCount names a kind and how many of its rows a selection left out.
type KindCount struct {
	Kind  string
	Group string
	Count int
}

// FeedSelection is SelectFeed's result.
type FeedSelection struct {
	Changes []issuesapi.RecentChange
	// Churn lists kinds whose every row was left out as controller churn.
	Churn []KindCount
	// Crowded lists kinds with rows left out so other kinds' rows fit.
	Crowded []KindCount
}

// SelectFeed picks the default feed's page from every ranked candidate. A
// kind outside the curated set whose rows mark it as controller churn (see
// churnGuard) is left out whole. Then each kind shows at most perKindShown rows while
// rows of other kinds are left out; slots no other kind fills go back to
// those kinds in rank order, so a window with one busy kind still fills the
// page.
func SelectFeed(changes []issuesapi.RecentChange, limit int) FeedSelection {
	RankAndCap(&changes, 0)
	var sel FeedSelection
	churn := churnGuard(changes)
	candidates := changes[:0:0]
	for _, c := range changes {
		if _, ok := churn[changeGroupKind(c)]; !ok {
			candidates = append(candidates, c)
		}
	}
	for key, n := range churn {
		sel.Churn = append(sel.Churn, KindCount{Kind: key.Kind, Group: key.Group, Count: n})
	}
	sortKindCounts(sel.Churn)

	if limit <= 0 || len(candidates) <= limit {
		sel.Changes = candidates
		return sel
	}
	shown := map[resourceid.GroupKind]int{}
	keep := make([]bool, len(candidates))
	kept := 0
	for i, c := range candidates {
		if kept == limit {
			break
		}
		key := changeGroupKind(c)
		if shown[key] < perKindShown {
			shown[key]++
			keep[i] = true
			kept++
		}
	}
	for i := range candidates {
		if kept == limit {
			break
		}
		if !keep[i] {
			keep[i] = true
			kept++
		}
	}
	lastKept := 0
	for i := range candidates {
		if keep[i] {
			lastKept = i
		}
	}
	// A kind with a row left out while a lower-ranked row was kept made
	// room for another kind; past lastKept, rows fell to the page limit
	// alone. A crowded kind counts all its rows left out.
	crowded := map[resourceid.GroupKind]bool{}
	left := map[resourceid.GroupKind]int{}
	for i, c := range candidates {
		if keep[i] {
			sel.Changes = append(sel.Changes, c)
			continue
		}
		key := changeGroupKind(c)
		left[key]++
		crowded[key] = crowded[key] || i < lastKept
	}
	for key, isCrowded := range crowded {
		if isCrowded {
			sel.Crowded = append(sel.Crowded, KindCount{Kind: key.Kind, Group: key.Group, Count: left[key]})
		}
	}
	sortKindCounts(sel.Crowded)
	return sel
}

// churnGuard returns the kinds outside the curated set whose rows mark them
// as controller churn, with their counts.
func churnGuard(changes []issuesapi.RecentChange) map[resourceid.GroupKind]int {
	counts := map[resourceid.GroupKind]int{}
	objects := map[resourceid.GroupKind]map[string]bool{}
	for _, c := range changes {
		key := changeGroupKind(c)
		if curatedGroupKind(key) {
			continue
		}
		counts[key]++
		if objects[key] == nil {
			objects[key] = map[string]bool{}
		}
		objects[key][c.Namespace+"/"+c.Name] = true
	}
	out := map[resourceid.GroupKind]int{}
	for key, n := range counts {
		if n >= churnMinRows && n >= churnRowsPerObject*len(objects[key]) {
			out[key] = n
		}
	}
	return out
}

func curatedGroupKind(key resourceid.GroupKind) bool {
	return (key.Kind == "Secret" && key.Group == "") || TrackedKindForGroup(key.Kind, key.Group)
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

// LeftOut is how many rows the selection left out and FeedSelectionGuidance
// names: Churn's and Crowded's.
func (sel FeedSelection) LeftOut() int {
	n := 0
	for _, list := range [][]KindCount{sel.Churn, sel.Crowded} {
		for _, k := range list {
			n += k.Count
		}
	}
	return n
}

// FeedSelectionGuidance is the narrowHint text naming the kinds SelectFeed
// left rows out of, or "".
func FeedSelectionGuidance(sel FeedSelection) string {
	if len(sel.Churn) == 0 && len(sel.Crowded) == 0 {
		return ""
	}
	var parts []string
	if len(sel.Churn) > 0 {
		parts = append(parts, fmt.Sprintf("Left out as likely controller churn, the same objects changed over and over: %s.", kindCountList(sel.Churn)))
	}
	if len(sel.Crowded) > 0 {
		parts = append(parts, fmt.Sprintf("Left out so more kinds fit on the page: %s.", kindCountList(sel.Crowded)))
	}
	all := append(slices.Clone(sel.Churn), sel.Crowded...)
	pass := "kind=<Kind>"
	if len(all) == 1 {
		pass = "kind=" + all[0].Kind
	}
	return strings.Join(parts, " ") + " Pass " + pass + " to list them."
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
