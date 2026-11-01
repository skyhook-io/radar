package meaningfulchanges

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

const (
	// omittedKindsScanBound is how many of the window's newest custom-resource
	// rows the omitted-kinds scan reads, the same bound the custom-resource
	// scan of an explicit query uses. Node rows have their own read.
	omittedKindsScanBound = 4 * FetchBound
	omittedKindsNamed     = 5
)

// runtimeGeneratedKinds are custom kinds controllers create and delete as
// routine bookkeeping without owner references, so their lifecycle says
// nothing about an incident. Their desired-state edits still count.
var runtimeGeneratedKinds = map[resourceid.GroupKind]bool{
	// Cilium allocates identities and endpoints as pods come and go.
	{Group: "cilium.io", Kind: "CiliumIdentity"}:      true,
	{Group: "cilium.io", Kind: "CiliumEndpoint"}:      true,
	{Group: "cilium.io", Kind: "CiliumEndpointSlice"}: true,
	// Calico IPAM allocations, through its CRDs or its API server.
	{Group: "crd.projectcalico.org", Kind: "IPAMBlock"}:     true,
	{Group: "crd.projectcalico.org", Kind: "IPAMHandle"}:    true,
	{Group: "crd.projectcalico.org", Kind: "BlockAffinity"}: true,
	{Group: "projectcalico.org", Kind: "IPAMBlock"}:         true,
	{Group: "projectcalico.org", Kind: "BlockAffinity"}:     true,
	// Policy engine reports and requests.
	{Group: "wgpolicyk8s.io", Kind: "PolicyReport"}:               true,
	{Group: "wgpolicyk8s.io", Kind: "ClusterPolicyReport"}:        true,
	{Group: "reports.kyverno.io", Kind: "EphemeralReport"}:        true,
	{Group: "reports.kyverno.io", Kind: "ClusterEphemeralReport"}: true,
	{Group: "kyverno.io", Kind: "UpdateRequest"}:                  true,
	// Provisioned per snapshot.
	{Group: "snapshot.storage.k8s.io", Kind: "VolumeSnapshotContent"}: true,
}

// OmittedKind is a kind the default feed left out that changed in the
// window, with its newest counted change.
type OmittedKind struct {
	Kind string `json:"kind"`
	// Group is the kind's API group; empty for the core group (Node).
	Group string `json:"group,omitempty"`
	// LatestChangeType is the newest counted change's type: add, update or
	// delete, as a change row's changeType.
	LatestChangeType string `json:"latestChangeType"`
	// LatestTimestamp is when that change was recorded (RFC 3339).
	LatestTimestamp string `json:"latestTimestamp"`
}

// OmittedKinds returns the kinds outside the default feed that had changes in
// the query's window and scope which a kind= query would list: desired-state
// edits to Nodes, and desired-state edits, creates and deletes of untracked
// custom resources. Rows owned by another object (derived custom resources
// such as CiliumEndpoints) and status-only changes are left out, so routine
// churn names nothing; so do creates and deletes of runtimeGeneratedKinds.
// Kinds with desired-state edits come first, then by how many rows they had.
//
// Built-in kinds and the custom kinds the default feed tracks are excluded
// in the store query, before its limit, so their churn can't use up the
// read. It still reads only the window's newest omittedKindsScanBound
// custom-resource rows, so in a busy window it can miss kinds; an absent
// result makes no claim. q must be a default query (no kind or name).
func OmittedKinds(ctx context.Context, q Query) ([]OmittedKind, error) {
	store := timeline.GetStore()
	if store == nil {
		return nil, timeline.StoreMissingError()
	}
	q = normalizeQuery(q)
	base := timeline.QueryOptions{
		Namespaces:     q.Namespaces,
		Since:          q.start,
		Sources:        []timeline.EventSource{timeline.SourceInformer},
		EventTypes:     append(append([]timeline.EventType{}, updateEventTypes...), lifecycleEventTypes...),
		ClusterContext: k8s.ActiveClusterContext(),
		IncludeManaged: false,
	}
	var events []timeline.TimelineEvent
	if len(q.Namespaces) == 0 {
		nodes := base
		nodes.Kinds, nodes.APIGroups, nodes.Limit = []string{"Node"}, []string{""}, FetchBound
		got, err := store.Query(ctx, nodes)
		if err != nil {
			return nil, err
		}
		events = append(events, got...)
	}
	custom := base
	custom.RequireAPIVersion = true
	custom.ExcludeAPIGroups = k8score.BuiltInAPIGroups()
	custom.ExcludeGroupKinds = trackedCustomGroupKinds()
	custom.Limit = omittedKindsScanBound
	got, err := store.Query(ctx, custom)
	if err != nil {
		return nil, err
	}
	events = append(events, got...)

	type tally struct {
		OmittedKind
		edits, lifecycle int
		latest           time.Time
	}
	byKind := map[resourceid.GroupKind]*tally{}
	for _, e := range events {
		category := omittedCategory(e)
		if category == "" {
			continue
		}
		if q.Allow != nil && !q.Allow(fromEvent(e, 1)) {
			continue
		}
		key := resourceid.GroupKind{Group: resourceid.GroupFromAPIVersion(e.APIVersion), Kind: e.Kind}
		t := byKind[key]
		if t == nil {
			t = &tally{OmittedKind: OmittedKind{Kind: key.Kind, Group: key.Group}}
			byKind[key] = t
		}
		if category == issuesapi.ChangeCategorySpecConfig {
			t.edits++
		} else {
			t.lifecycle++
		}
		if e.Timestamp.After(t.latest) {
			t.latest = e.Timestamp
			t.LatestChangeType = string(e.EventType)
			t.LatestTimestamp = e.Timestamp.Format(time.RFC3339)
		}
	}
	tallies := make([]*tally, 0, len(byKind))
	for _, t := range byKind {
		tallies = append(tallies, t)
	}
	sort.Slice(tallies, func(i, j int) bool {
		a, b := tallies[i], tallies[j]
		if (a.edits > 0) != (b.edits > 0) {
			return a.edits > 0
		}
		if a.edits+a.lifecycle != b.edits+b.lifecycle {
			return a.edits+a.lifecycle > b.edits+b.lifecycle
		}
		return a.Kind+"."+a.Group < b.Kind+"."+b.Group
	})
	out := make([]OmittedKind, len(tallies))
	for i, t := range tallies {
		out[i] = t.OmittedKind
	}
	return out, nil
}

// trackedCustomGroupKinds are the custom kinds the default feed already
// lists, by the group it records them from.
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

// omittedCategory returns the category an explicit kind= query would list the
// row under when the default feed leaves it out and the hint counts it, or "".
func omittedCategory(e timeline.TimelineEvent) issuesapi.ChangeCategory {
	if e.IsManaged() {
		return ""
	}
	category, _ := classify(e)
	switch {
	case category == issuesapi.ChangeCategorySpecConfig:
		if (e.Kind == "Node" && e.Namespace == "") || isUntrackedCustomResource(e) {
			return category
		}
	case category == issuesapi.ChangeCategoryLifecycle:
		group := resourceid.GroupFromAPIVersion(e.APIVersion)
		if isUntrackedCustomResource(e) && !runtimeGeneratedKinds[resourceid.GroupKind{Group: group, Kind: e.Kind}] {
			return category
		}
	}
	return ""
}

func isUntrackedCustomResource(e timeline.TimelineEvent) bool {
	return isCustomResourceEvent(e) && !(TrackedKind(e.Kind) && eventGroupMatchesTracked(e.Kind, e.APIVersion))
}

// OmittedKindsGuidance is the narrowHint sentence naming OmittedKinds'
// result, or "" when it names no kind. The investigation UI removes it from
// narrowHint by its wording and shows omittedKinds instead
// (web/src/components/diagnose/investigationEvidence/adapters/changes.ts), so
// a change to the wording must change it there too.
func OmittedKindsGuidance(kinds []OmittedKind) string {
	if len(kinds) == 0 {
		return ""
	}
	names := make([]string, 0, omittedKindsNamed)
	for _, k := range kinds[:min(len(kinds), omittedKindsNamed)] {
		names = append(names, k.Kind)
	}
	more := ""
	if len(kinds) > omittedKindsNamed {
		more = fmt.Sprintf(" and %d more", len(kinds)-omittedKindsNamed)
	}
	return fmt.Sprintf("This window also has changes to kinds the default feed leaves out: %s%s. Pass kind=<Kind> to list them.", strings.Join(names, ", "), more)
}

// OmittedKindsErrorGuidance is the narrowHint sentence when the omitted-kinds
// scan failed.
func OmittedKindsErrorGuidance(err error) string {
	return "Radar could not check for changes to kinds the default feed leaves out (" + err.Error() + "); pass kind=<Kind> to check one."
}
