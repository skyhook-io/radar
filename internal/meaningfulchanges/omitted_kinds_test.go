package meaningfulchanges

import (
	"context"
	"reflect"
	"strings"
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
			ID: kind + name + string(rune('a'+n)), Kind: kind, APIVersion: apiVersion, Namespace: namespace, Name: name,
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

	kinds, full, err := OmittedKinds(context.Background(), Query{})
	if err != nil || full {
		t.Fatalf("OmittedKinds: full=%v err=%v", full, err)
	}
	// Desired-state edits first: Node's one edit outranks BGPConfiguration's
	// create.
	if want := []string{"TidbCluster", "Node", "BGPConfiguration"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("cluster-wide kinds = %v, want %v", kinds, want)
	}

	// A namespace query names only that namespace's kinds: no Node, and no
	// count of what changed elsewhere.
	kinds, _, _ = OmittedKinds(context.Background(), Query{Namespaces: []string{"db"}})
	if want := []string{"TidbCluster"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("namespace kinds = %v, want %v", kinds, want)
	}

	// Kinds the caller may not read are not named.
	kinds, _, _ = OmittedKinds(context.Background(), Query{Allow: func(c issuesapi.RecentChange) bool { return c.Kind != "Node" }})
	if want := []string{"TidbCluster", "BGPConfiguration"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("filtered kinds = %v, want %v", kinds, want)
	}

	if got := OmittedKindsGuidance(nil, true); got != "" {
		t.Fatalf("guidance for no kinds = %q", got)
	}
}

func TestOmittedKindsGuidanceCapsNames(t *testing.T) {
	got := OmittedKindsGuidance([]string{"A", "B", "C", "D", "E", "F", "G"}, true)
	if !strings.Contains(got, "leaves out: A, B, C, D, E and 2 more. Pass kind=<Kind>") || !strings.Contains(got, "newest 4000 changes") {
		t.Fatalf("guidance = %q", got)
	}
}
