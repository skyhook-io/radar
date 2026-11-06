package meaningfulchanges

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func desiredDiff(paths ...string) *timeline.DiffInfo {
	diff := &timeline.DiffInfo{}
	for _, p := range paths {
		diff.Fields = append(diff.Fields, timeline.FieldChange{Path: p, OldValue: "a", NewValue: "b"})
	}
	return diff
}

func otherKindsQuery(namespaces ...string) Query {
	return Query{Namespaces: namespaces, OtherKinds: true}
}

func changeNames(changes []issuesapi.RecentChange) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, c.Kind+"/"+c.Name)
	}
	return out
}

// Rows the other-kinds read admits and leaves out, by kind and ownership.
func TestRecentOtherKindsCoverage(t *testing.T) {
	store := initFetchBoundStore(t, 1000)
	now := time.Now()
	at := func(i int) time.Time { return now.Add(-time.Duration(30-i) * time.Minute) }
	owner := &timeline.OwnerInfo{Kind: "Gateway", Name: "gw"}
	for i, e := range []timeline.TimelineEvent{
		{ID: "dr-edit", APIVersion: "networking.istio.io/v1", Kind: "DestinationRule", Name: "reviews", EventType: timeline.EventTypeUpdate, Diff: desiredDiff("spec.trafficPolicy.loadBalancer.simple", "metadata.generation")},
		{ID: "dr-status", APIVersion: "networking.istio.io/v1", Kind: "DestinationRule", Name: "ratings", EventType: timeline.EventTypeUpdate, Diff: desiredDiff("status.conditions[Reconciled]")},
		{ID: "owned-cr", APIVersion: "networking.istio.io/v1", Kind: "VirtualService", Name: "owned", EventType: timeline.EventTypeUpdate, Diff: desiredDiff("spec.hosts"), Owner: owner},
		{ID: "denied", APIVersion: "cert-manager.io/v1", Kind: "CertificateRequest", Name: "web-1", EventType: timeline.EventTypeAdd},
		{ID: "tracked", APIVersion: "argoproj.io/v1alpha1", Kind: "Application", Name: "shop", EventType: timeline.EventTypeUpdate, Diff: desiredDiff("spec.source.targetRevision")},
		{ID: "builtin", APIVersion: "v1", Kind: "ServiceAccount", Name: "default", EventType: timeline.EventTypeAdd},
	} {
		e.Timestamp = at(i)
		appendEvent(t, store, e)
	}
	// Cluster-scoped rows; appendEvent would give them a namespace.
	for i, e := range []timeline.TimelineEvent{
		{ID: "bgp", APIVersion: "crd.projectcalico.org/v1", Kind: "BGPConfiguration", Name: "default", EventType: timeline.EventTypeAdd},
		{ID: "cordon", APIVersion: "v1", Kind: "Node", Name: "worker-1", EventType: timeline.EventTypeUpdate, Diff: desiredDiff("spec.unschedulable"), Owner: &timeline.OwnerInfo{Kind: "NodeClaim", Name: "nc"}},
		{ID: "node-add", APIVersion: "v1", Kind: "Node", Name: "worker-2", EventType: timeline.EventTypeAdd},
		{ID: "node-not-ready", APIVersion: "v1", Kind: "Node", Name: "worker-3", EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{
			{Path: "spec.taints", OldValue: []any{}, NewValue: []any{"node.kubernetes.io/not-ready"}}, {Path: "status.conditions[Ready]", OldValue: "True", NewValue: "False"},
		}}},
	} {
		e.Timestamp, e.Source, e.ClusterContext = at(10+i), timeline.SourceInformer, k8s.ActiveClusterContext()
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}

	result, err := Recent(context.Background(), otherKindsQuery())
	if err != nil {
		t.Fatal(err)
	}
	got := changeNames(slices.Concat(result.Changes, result.Other))
	for _, want := range []string{"DestinationRule/reviews", "BGPConfiguration/default", "Node/worker-1", "Node/worker-2", "Application/shop"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	for _, unwanted := range []string{"DestinationRule/ratings", "VirtualService/owned", "CertificateRequest/web-1", "ServiceAccount/default", "Node/worker-3"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("%s must stay out of the default feed: %v", unwanted, got)
		}
	}
	if n := strings.Count(strings.Join(got, ","), "Application/shop"); n != 1 {
		t.Errorf("curated custom kind listed %d times: %v", n, got)
	}

	// Nodes are cluster-scoped: a namespace query has none.
	result, err = Recent(context.Background(), otherKindsQuery("shop"))
	if err != nil {
		t.Fatal(err)
	}
	got = changeNames(slices.Concat(result.Changes, result.Other))
	if !slices.Contains(got, "DestinationRule/reviews") || slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "Node/") || strings.HasPrefix(s, "BGPConfiguration/") }) {
		t.Errorf("namespace query = %v, want its custom resources and no cluster-scoped rows", got)
	}

	// The read is off without OtherKinds and for kind or name queries.
	result, err = Recent(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if got := changeNames(slices.Concat(result.Changes, result.Other)); slices.Contains(got, "DestinationRule/reviews") {
		t.Errorf("curated feed lists %v", got)
	}
}

// A controller's status writes cannot use up the read before a desired-state
// edit of the same kind.
func TestRecentOtherKindsStatusBurstKeepsDesiredEdit(t *testing.T) {
	store := initFetchBoundStore(t, 6000)
	now := time.Now()
	appendEvent(t, store, timeline.TimelineEvent{
		ID: "edit", Timestamp: now.Add(-50 * time.Minute), APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: "worker",
		EventType: timeline.EventTypeUpdate, Diff: desiredDiff("spec.maxReplicaCount", "metadata.generation"),
	})
	for i := range 5000 {
		appendEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("status-%d", i), Timestamp: now.Add(-40*time.Minute + time.Duration(i)*100*time.Millisecond),
			APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: fmt.Sprintf("so-%d", i%30),
			EventType: timeline.EventTypeUpdate, Diff: desiredDiff("status.conditions[Active]"),
		})
	}
	result, err := Recent(context.Background(), otherKindsQuery())
	if err != nil {
		t.Fatal(err)
	}
	if result.OtherFetchSaturated {
		t.Error("status rows must not count against the read's bound")
	}
	if got := changeNames(result.Other); !slices.Contains(got, "ScaledObject/worker") {
		t.Fatalf("Other = %v, want the ScaledObject edit", got)
	}
}

func change(kind, apiVersion, name string, minute int) issuesapi.RecentChange {
	return issuesapi.RecentChange{
		Kind: kind, APIVersion: apiVersion, Namespace: "shop", Name: name, ChangeType: "update",
		ChangeCategory: issuesapi.ChangeCategorySpecConfig, Timestamp: time.Date(2026, 11, 2, 10, minute, 0, 0, time.UTC).Format(time.RFC3339),
	}
}

func curatedRows(n int) []issuesapi.RecentChange {
	rows := make([]issuesapi.RecentChange, 0, n)
	for i := range n {
		rows = append(rows, change("Deployment", "apps/v1", fmt.Sprintf("web-%02d", i), 59-i%60))
	}
	return rows
}

func TestRecentKeepsOtherKindsApart(t *testing.T) {
	store := initFetchBoundStore(t, 1000)
	now := time.Now()
	appendEvent(t, store, timeline.TimelineEvent{
		ID: "dr", Timestamp: now.Add(-time.Minute), APIVersion: "networking.istio.io/v1", Kind: "DestinationRule", Name: "reviews",
		EventType: timeline.EventTypeUpdate, Diff: desiredDiff("spec.trafficPolicy"),
	})
	appendEvent(t, store, timeline.TimelineEvent{
		ID: "cm", Timestamp: now.Add(-2 * time.Minute), APIVersion: "v1", Kind: "ConfigMap", Name: "cfg",
		EventType: timeline.EventTypeUpdate, Diff: desiredDiff("data.key"),
	})
	result, err := Recent(context.Background(), otherKindsQuery())
	if err != nil {
		t.Fatal(err)
	}
	if got := changeNames(result.Changes); !slices.Equal(got, []string{"ConfigMap/cfg"}) {
		t.Fatalf("Changes = %v, want the curated row alone", got)
	}
	if got := changeNames(result.Other); !slices.Equal(got, []string{"DestinationRule/reviews"}) {
		t.Fatalf("Other = %v", got)
	}
}

// Whatever the other kinds hold, a page 5 rows longer than today's keeps
// every curated row today's page lists, in order.
func TestComposeFeedKeepsTodaysCuratedPage(t *testing.T) {
	curated := curatedRows(40)
	var other []issuesapi.RecentChange
	for i := range 30 {
		other = append(other, change(fmt.Sprintf("Kind%d", i%7), "example.com/v1", fmt.Sprintf("o-%d", i), 59))
		other = append(other, change("Node", "v1", fmt.Sprintf("n-%d", i), 59))
	}
	today := slices.Clone(curated)
	RankAndCap(&today, 20)

	// Helm rows join after the timeline's, out of order.
	slices.Reverse(curated)
	sel := ComposeFeed(curated, other, 25)
	if len(sel.Changes) != 25 || sel.CuratedShown != 20 {
		t.Fatalf("page %d rows, %d curated; want 25 and 20", len(sel.Changes), sel.CuratedShown)
	}
	var kept []string
	for _, name := range changeNames(sel.Changes) {
		if strings.HasPrefix(name, "Deployment/") {
			kept = append(kept, name)
		}
	}
	if !slices.Equal(kept, changeNames(today)) {
		t.Fatalf("curated rows = %v, want %v", kept, changeNames(today))
	}
}

func TestComposeFeedReserve(t *testing.T) {
	var other []issuesapi.RecentChange
	for i := range 10 {
		other = append(other, change(fmt.Sprintf("Kind%d", i), "example.com/v1", "a", 59))
	}
	for _, tc := range []struct{ limit, wantOther int }{{1, 0}, {4, 0}, {5, 1}, {20, 4}, {25, 5}, {50, 5}} {
		sel := ComposeFeed(curatedRows(60), other, tc.limit)
		if got := len(sel.Changes) - sel.CuratedShown; len(sel.Changes) != tc.limit || got != tc.wantOther {
			t.Errorf("limit %d: %d rows, %d other; want %d other", tc.limit, len(sel.Changes), got, tc.wantOther)
		}
	}
}

// One busy kind holds two reserved slots; the rest stay with curated rows.
func TestComposeFeedReservedPerKind(t *testing.T) {
	var loop []issuesapi.RecentChange
	for i := range 50 {
		loop = append(loop, change("Widget", "example.com/v1", "w", i))
	}
	sel := ComposeFeed(curatedRows(40), loop, 25)
	if sel.CuratedShown != 23 {
		t.Fatalf("curated rows = %d, want 23", sel.CuratedShown)
	}
	if hint := FeedSelectionGuidance(sel); !strings.Contains(hint, "48 Widget changes") || !strings.Contains(hint, "kind=Widget") {
		t.Fatalf("hint = %q", hint)
	}
}

// Spare slots go kind by kind, so a controller loop's repeats come after
// every other kind's rows, and it still shows.
func TestComposeFeedLoopDoesNotBuryOtherKinds(t *testing.T) {
	var other []issuesapi.RecentChange
	for i := range 50 {
		other = append(other, change("Widget", "example.com/v1", "w", 30+i%30))
	}
	for i := range 5 {
		other = append(other, change(fmt.Sprintf("Kind%d", i), "example.com/v1", "a", i))
	}
	sel := ComposeFeed(curatedRows(1), other, 25)
	if len(sel.Changes) != 25 {
		t.Fatalf("page %d rows, want 25", len(sel.Changes))
	}
	got := changeNames(sel.Changes)
	for _, want := range []string{"Deployment/web-00", "Widget/w", "Kind0/a", "Kind1/a", "Kind2/a", "Kind3/a", "Kind4/a"} {
		if !slices.Contains(got, want) {
			t.Fatalf("page = %v, want %s", got, want)
		}
	}
}

// A page sorted by the normal ranking puts an other-kinds edit among curated
// edits, not after them.
func TestComposeFeedRanksThePage(t *testing.T) {
	sel := ComposeFeed(curatedRows(3), []issuesapi.RecentChange{change("Widget", "example.com/v1", "w", 59)}, 25)
	if got := changeNames(sel.Changes); got[0] != "Deployment/web-00" || got[1] != "Widget/w" {
		t.Fatalf("page = %v", got)
	}
}

// Same kind name in another group is another kind.
func TestComposeFeedKindsByGroup(t *testing.T) {
	other := []issuesapi.RecentChange{change("Widget", "a.example.com/v1", "a", 3), change("Widget", "a.example.com/v1", "b", 2), change("Widget", "b.example.com/v1", "c", 1)}
	sel := ComposeFeed(nil, other, 2)
	if got := changeNames(sel.Changes); !slices.Contains(got, "Widget/c") {
		t.Fatalf("page = %v", got)
	}
}

// Bare "spec" comes from a generic diff of a kind Radar can't name fields for.
// Curated kinds keep their rule, whatever a same-named custom kind does.
func TestClassifyBareSpec(t *testing.T) {
	for _, tc := range []struct {
		name, kind, apiVersion string
		want                   issuesapi.ChangeCategory
	}{
		{"custom kind", "Widget", "example.com/v1", issuesapi.ChangeCategorySpecConfig},
		{"curated custom kind", "Application", "argoproj.io/v1alpha1", ""},
		{"curated built-in", "Deployment", "apps/v1", ""},
		{"curated kind, group not recorded", "Deployment", "", ""},
		{"custom kind named like a curated one", "Deployment", "example.com/v1", issuesapi.ChangeCategorySpecConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := classify(timeline.TimelineEvent{
				Source: timeline.SourceInformer, Kind: tc.kind, APIVersion: tc.apiVersion, EventType: timeline.EventTypeUpdate, Diff: desiredDiff("spec"),
			})
			if got != tc.want {
				t.Fatalf("classify = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOnlyControllerNodeChanges(t *testing.T) {
	node := func(fields ...timeline.FieldChange) timeline.TimelineEvent {
		return timeline.TimelineEvent{Kind: "Node", APIVersion: "v1", EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: fields}}
	}
	keys := func(old, new []any) timeline.FieldChange {
		return timeline.FieldChange{Path: "spec.taints", OldValue: old, NewValue: new}
	}
	for _, tt := range []struct {
		name string
		e    timeline.TimelineEvent
		want bool
	}{
		{"not-ready added", node(keys([]any{}, []any{"node.kubernetes.io/not-ready"}), timeline.FieldChange{Path: "status.conditions[Ready]"}), true},
		{"unreachable re-valued", node(timeline.FieldChange{Path: "spec.taints[node.kubernetes.io/unreachable]"}), true},
		{"custom taint beside not-ready", node(keys([]any{"node.kubernetes.io/not-ready"}, []any{"dedicated"})), false},
		{"karpenter disrupted", node(keys([]any{}, []any{"karpenter.sh/disrupted"})), true},
		{"autoscaler removal", node(keys([]any{}, []any{"ToBeDeletedByClusterAutoscaler"})), true},
		{"karpenter disrupted beside a custom taint", node(keys([]any{"dedicated"}, []any{"karpenter.sh/disrupted"})), false},
		{"cilium startup taint", node(keys([]any{"node.cilium.io/agent-not-ready"}, []any{})), true},
		{"karpenter registration", node(keys([]any{"karpenter.sh/unregistered"}, []any{})), true},
		{"cordon with its taint", node(timeline.FieldChange{Path: "spec.unschedulable"}, keys([]any{}, []any{"node.kubernetes.io/unschedulable"})), false},
		{"label", node(timeline.FieldChange{Path: "metadata.labels[team]"}), false},
		{"removal label with removal taint", node(keys([]any{}, []any{"karpenter.sh/disrupted"}), timeline.FieldChange{Path: "metadata.labels[node.kubernetes.io/exclude-from-external-load-balancers]"}), true},
		{"unreadable keys", node(timeline.FieldChange{Path: "spec.taints", OldValue: "x", NewValue: "y"}), false},
		{"status only", node(timeline.FieldChange{Path: "status.conditions[Ready]"}), false},
	} {
		if got := onlyControllerNodeChanges(tt.e); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}
