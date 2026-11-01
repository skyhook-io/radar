package meaningfulchanges

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestOmittedKindsNamesChangesTheDefaultFeedLeavesOut(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	n := 0
	add := func(kind, apiVersion, namespace, name string, eventType timeline.EventType, owner *timeline.OwnerInfo, paths ...string) {
		t.Helper()
		n++
		e := timeline.TimelineEvent{
			ID: fmt.Sprintf("%s/%s/%d", kind, name, n), Kind: kind, APIVersion: apiVersion, Namespace: namespace, Name: name,
			Timestamp: now.Add(-time.Duration(n) * time.Second), Source: timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(), EventType: eventType, Owner: owner,
		}
		if len(paths) > 0 {
			e.Diff = &timeline.DiffInfo{}
			for _, p := range paths {
				e.Diff.Fields = append(e.Diff.Fields, timeline.FieldChange{Path: p, NewValue: "x"})
			}
		}
		if err := timeline.GetStore().Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	// Listed: a Node taint edit, an untracked custom resource's create and
	// spec edit (cluster-scoped and namespaced).
	add("Node", "v1", "", "worker-1", timeline.EventTypeUpdate, nil, "spec.taints")
	add("BGPConfiguration", "crd.projectcalico.org/v1", "", "default", timeline.EventTypeAdd, nil)
	add("TidbCluster", "pingcap.com/v1alpha1", "db", "basic", timeline.EventTypeUpdate, nil, "spec.tikv.replicas")
	add("TidbCluster", "pingcap.com/v1alpha1", "db", "basic", timeline.EventTypeUpdate, nil, "spec.pd.replicas")
	// Not listed: Node lifecycle and status, owned or status-only custom
	// resources, runtime-generated lifecycle churn, tracked custom kinds and
	// default-feed kinds.
	add("Node", "v1", "", "worker-2", timeline.EventTypeAdd, nil)
	add("Node", "v1", "", "worker-1", timeline.EventTypeUpdate, nil, "status.conditions.Ready")
	add("CiliumEndpoint", "cilium.io/v2", "db", "pod-a", timeline.EventTypeAdd, &timeline.OwnerInfo{Kind: "Pod", Name: "pod-a"})
	add("CiliumIdentity", "cilium.io/v2", "", "12345", timeline.EventTypeAdd, nil)
	add("CiliumIdentity", "cilium.io/v2", "", "12346", timeline.EventTypeDelete, nil)
	add("CiliumIdentity", "cilium.io/v2", "", "12347", timeline.EventTypeAdd, nil)
	add("Certificate", "cert-manager.io/v1", "db", "tls", timeline.EventTypeUpdate, nil, "status.notAfter")
	add("Application", "argoproj.io/v1alpha1", "argocd", "shop", timeline.EventTypeUpdate, nil, "spec.source.targetRevision")
	add("Deployment", "apps/v1", "db", "web", timeline.EventTypeUpdate, nil, "spec.replicas")

	kinds, err := OmittedKinds(context.Background(), Query{})
	if err != nil {
		t.Fatalf("OmittedKinds: %v", err)
	}
	// Desired-state edits first: Node's one edit outranks BGPConfiguration's
	// create. Each entry carries its group and newest counted change.
	if got := kinds[0]; got.Group != "pingcap.com" || got.LatestChangeType != "update" || got.LatestTimestamp != now.Add(-3*time.Second).Format(time.RFC3339) {
		t.Fatalf("TidbCluster entry = %+v", got)
	}
	if got := kinds[1]; got.Group != "" || got.Kind != "Node" {
		t.Fatalf("Node entry = %+v", got)
	}
	if want := []string{"TidbCluster", "Node", "BGPConfiguration"}; !reflect.DeepEqual(omittedNames(kinds), want) {
		t.Fatalf("cluster-wide kinds = %v, want %v", kinds, want)
	}

	// A namespace query names only that namespace's kinds: no Node, and no
	// count of what changed elsewhere.
	kinds, _ = OmittedKinds(context.Background(), Query{Namespaces: []string{"db"}})
	if want := []string{"TidbCluster"}; !reflect.DeepEqual(omittedNames(kinds), want) {
		t.Fatalf("namespace kinds = %v, want %v", kinds, want)
	}

	// Kinds the caller may not read are not named.
	kinds, _ = OmittedKinds(context.Background(), Query{Allow: func(c issuesapi.RecentChange) bool { return c.Kind != "Node" }})
	if want := []string{"TidbCluster", "BGPConfiguration"}; !reflect.DeepEqual(omittedNames(kinds), want) {
		t.Fatalf("filtered kinds = %v, want %v", kinds, want)
	}

	if got := OmittedKindsGuidance(nil); got != "" {
		t.Fatalf("guidance for no kinds = %q", got)
	}
}

func omittedNames(kinds []OmittedKind) []string {
	var out []string
	for _, k := range kinds {
		out = append(out, k.Kind)
	}
	return out
}

// Tracked GitOps churn is filtered in the store query, so it can't use up
// the scan's budget and hide an older untracked change.
func TestOmittedKindsTrackedChurnDoesNotUseTheBudget(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: omittedKindsScanBound + 1000}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	appendRow := func(id, kind, apiVersion string, at time.Time, eventType timeline.EventType, paths ...string) {
		t.Helper()
		e := timeline.TimelineEvent{ID: id, Kind: kind, APIVersion: apiVersion, Name: id, Namespace: "argocd", Timestamp: at,
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(), EventType: eventType}
		for _, p := range paths {
			if e.Diff == nil {
				e.Diff = &timeline.DiffInfo{}
			}
			e.Diff.Fields = append(e.Diff.Fields, timeline.FieldChange{Path: p, NewValue: "x"})
		}
		if err := timeline.GetStore().Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	appendRow("bgp", "BGPConfiguration", "crd.projectcalico.org/v1", now.Add(-30*time.Minute), timeline.EventTypeAdd)
	for i := 0; i < omittedKindsScanBound+10; i++ {
		appendRow(fmt.Sprintf("app-%d", i), "Application", "argoproj.io/v1alpha1", now.Add(-time.Duration(i)*time.Millisecond), timeline.EventTypeUpdate, "spec.source.targetRevision")
	}
	kinds, err := OmittedKinds(context.Background(), Query{})
	if err != nil || !reflect.DeepEqual(omittedNames(kinds), []string{"BGPConfiguration"}) {
		t.Fatalf("OmittedKinds = %+v, err=%v; want BGPConfiguration past the tracked churn", kinds, err)
	}
}

// Bookkeeping exclusions match group and kind: a same-named kind from
// another group still counts.
func TestOmittedKindsBookkeepingMatchesGroup(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatal(err)
	}
	for i, apiVersion := range []string{"cilium.io/v2", "example.com/v1"} {
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprint(i), Kind: "CiliumIdentity", APIVersion: apiVersion, Name: fmt.Sprint(i), Timestamp: time.Now(),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(), EventType: timeline.EventTypeAdd,
		}); err != nil {
			t.Fatal(err)
		}
	}
	kinds, _ := OmittedKinds(context.Background(), Query{})
	if len(kinds) != 1 || kinds[0].Group != "example.com" {
		t.Fatalf("OmittedKinds = %+v, want only example.com's CiliumIdentity", kinds)
	}
}

func TestOmittedKindsGuidanceCapsNames(t *testing.T) {
	var many []OmittedKind
	for _, k := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		many = append(many, OmittedKind{Kind: k})
	}
	got := OmittedKindsGuidance(many)
	if want := "This window also has changes to kinds the default feed leaves out: A, B, C, D, E and 2 more. Pass kind=<Kind> to list them."; got != want {
		t.Fatalf("guidance = %q, want %q", got, want)
	}
}
