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
	return Query{Namespaces: namespaces, OtherKinds: true, Uncapped: true}
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
	got := changeNames(result.Changes)
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
	got = changeNames(result.Changes)
	if !slices.Contains(got, "DestinationRule/reviews") || slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "Node/") || strings.HasPrefix(s, "BGPConfiguration/") }) {
		t.Errorf("namespace query = %v, want its custom resources and no cluster-scoped rows", got)
	}

	// The read is off without OtherKinds and for kind or name queries.
	result, err = Recent(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if got := changeNames(result.Changes); slices.Contains(got, "DestinationRule/reviews") {
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
	if result.FetchSaturated {
		t.Error("status rows must not count against the read's bound")
	}
	sel := SelectFeed(result.Changes, 20)
	if got := changeNames(sel.Changes); !slices.Contains(got, "ScaledObject/worker") {
		t.Fatalf("page = %v, want the ScaledObject edit", got)
	}
}

// Fifty ConfigMap edits rank like the custom resource's edit; the per-kind
// cap keeps a place for it, and the page names the ConfigMaps left out.
func TestSelectFeedKeepsOtherKindsBesideABusyKind(t *testing.T) {
	store := initFetchBoundStore(t, 1000)
	now := time.Now()
	appendEvent(t, store, timeline.TimelineEvent{
		ID: "dr", Timestamp: now.Add(-50 * time.Minute), APIVersion: "networking.istio.io/v1", Kind: "DestinationRule", Name: "reviews",
		EventType: timeline.EventTypeUpdate, Diff: desiredDiff("spec.trafficPolicy", "metadata.generation"),
	})
	for i := range 50 {
		appendEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("cm-%d", i), Timestamp: now.Add(-time.Duration(40-i/2) * time.Minute), APIVersion: "v1", Kind: "ConfigMap",
			Name: fmt.Sprintf("cfg-%d", i), EventType: timeline.EventTypeUpdate, Diff: desiredDiff("data.key"),
		})
	}
	result, err := Recent(context.Background(), otherKindsQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 51 || result.OutputCapped {
		t.Fatalf("uncapped Recent returned %d rows (capped %v), want all 51", len(result.Changes), result.OutputCapped)
	}
	sel := SelectFeed(result.Changes, 20)
	got := changeNames(sel.Changes)
	if len(got) != 20 || !slices.Contains(got, "DestinationRule/reviews") {
		t.Fatalf("page = %v, want 20 rows with the DestinationRule edit", got)
	}
	if len(sel.Crowded) != 1 || sel.Crowded[0].Kind != "ConfigMap" || sel.Crowded[0].Count != 31 {
		t.Fatalf("crowded = %+v, want 31 ConfigMap", sel.Crowded)
	}
	if hint := FeedSelectionGuidance(sel); !strings.Contains(hint, "31 ConfigMap") || !strings.Contains(hint, "kind=") {
		t.Fatalf("hint = %q", hint)
	}
}

func change(kind, apiVersion, name string, minute int) issuesapi.RecentChange {
	return issuesapi.RecentChange{
		Kind: kind, APIVersion: apiVersion, Namespace: "shop", Name: name, ChangeType: "update",
		ChangeCategory: issuesapi.ChangeCategorySpecConfig, Timestamp: time.Date(2026, 11, 2, 10, minute, 0, 0, time.UTC).Format(time.RFC3339),
	}
}

func TestSelectFeedPerKindCap(t *testing.T) {
	// One busy kind alone fills the page: no other kind needs the slots.
	var only []issuesapi.RecentChange
	for i := range 30 {
		only = append(only, change("ConfigMap", "v1", fmt.Sprintf("cfg-%d", i), i))
	}
	sel := SelectFeed(only, 20)
	if len(sel.Changes) != 20 || len(sel.Crowded) != 0 {
		t.Fatalf("single kind: %d rows, crowded %+v; want 20 and none", len(sel.Changes), sel.Crowded)
	}
	// The page limit alone, with every kind under the cap, names nothing.
	var few []issuesapi.RecentChange
	for i := range 10 {
		few = append(few, change(fmt.Sprintf("Kind%d", i), "example.com/v1", "a", i), change(fmt.Sprintf("Kind%d", i), "example.com/v1", "b", i))
	}
	sel = SelectFeed(few, 5)
	if len(sel.Changes) != 5 || len(sel.Crowded) != 0 {
		t.Fatalf("limit only: %d rows, crowded %+v", len(sel.Changes), sel.Crowded)
	}
	// Same kind name, another group: a different kind for the cap.
	var mixed []issuesapi.RecentChange
	for i := range 10 {
		mixed = append(mixed, change("Service", "v1", fmt.Sprintf("core-%d", i), 50+i))
	}
	mixed = append(mixed, change("Service", "serving.knative.dev/v1", "knative", 1))
	sel = SelectFeed(mixed, 4)
	if got := changeNames(sel.Changes); !slices.Contains(got, "Service/knative") {
		t.Fatalf("page = %v, want the Knative Service", got)
	}
}

// A kind outside the curated set with far more rows than the others is left
// out whole and named; a curated kind with the same count is not.
func TestSelectFeedChurnGuard(t *testing.T) {
	var changes []issuesapi.RecentChange
	for i := range 200 {
		changes = append(changes, change("Widget", "example.com/v1", fmt.Sprintf("w-%d", i%7), i%60))
		changes = append(changes, change("ConfigMap", "v1", fmt.Sprintf("cfg-%d", i%7), i%60))
	}
	for i := range 4 {
		changes = append(changes,
			change("DestinationRule", "networking.istio.io/v1", fmt.Sprintf("dr-%d", i), i),
			change("Deployment", "apps/v1", fmt.Sprintf("web-%d", i), i),
			change("NodePool", "karpenter.sh/v1", fmt.Sprintf("pool-%d", i), i))
	}
	sel := SelectFeed(changes, 20)
	if len(sel.Churn) != 1 || sel.Churn[0].Kind != "Widget" || sel.Churn[0].Count != 200 {
		t.Fatalf("churn = %+v, want 200 Widget", sel.Churn)
	}
	got := changeNames(sel.Changes)
	if slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "Widget/") }) {
		t.Fatalf("page lists churn: %v", got)
	}
	if !slices.Contains(got, "DestinationRule/dr-3") || !slices.Contains(got, "NodePool/pool-3") || !slices.Contains(got, "ConfigMap/cfg-0") {
		t.Fatalf("page = %v, want the other kinds", got)
	}
	if hint := FeedSelectionGuidance(sel); !strings.Contains(hint, "200 Widget") {
		t.Fatalf("hint = %q", hint)
	}

	// Below the absolute floor, a lopsided count is not churn.
	var small []issuesapi.RecentChange
	for i := range churnMinRows - 1 {
		small = append(small, change("Widget", "example.com/v1", "w", i%60))
	}
	small = append(small, change("Deployment", "apps/v1", "web", 1), change("Service", "v1", "web", 1), change("NodePool", "karpenter.sh/v1", "p", 1))
	if sel := SelectFeed(small, 20); len(sel.Churn) != 0 {
		t.Fatalf("churn below floor = %+v", sel.Churn)
	}
}

func TestOnlyReadinessTaintChanges(t *testing.T) {
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
		{"karpenter disrupted", node(keys([]any{}, []any{"karpenter.sh/disrupted"})), false},
		{"autoscaler removal", node(keys([]any{}, []any{"ToBeDeletedByClusterAutoscaler"})), false},
		{"cordon with its taint", node(timeline.FieldChange{Path: "spec.unschedulable"}, keys([]any{}, []any{"node.kubernetes.io/unschedulable"})), false},
		{"label", node(timeline.FieldChange{Path: "metadata.labels[team]"}), false},
		{"unreadable keys", node(timeline.FieldChange{Path: "spec.taints", OldValue: "x", NewValue: "y"}), false},
		{"status only", node(timeline.FieldChange{Path: "status.conditions[Ready]"}), false},
	} {
		if got := onlyReadinessTaintChanges(tt.e); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}
