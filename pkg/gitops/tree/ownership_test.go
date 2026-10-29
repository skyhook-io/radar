package tree

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func newTestBuilder(t *testing.T, getter DynamicGetter, topo *topology.Topology) *Builder {
	t.Helper()
	return NewBuilder(getter, topo).WithOwnership(ownershipSession(t))
}

func ownershipSession(t *testing.T, objects ...ownershiptest.Object) ownership.ReadSession {
	t.Helper()
	world := ownershiptest.World{Objects: objects}
	seen := map[resourceid.GroupKind]bool{}
	for _, obj := range objects {
		gk := obj.Ref.GroupKind()
		if seen[gk] {
			continue
		}
		seen[gk] = true
		version := obj.APIVersion
		if i := strings.LastIndex(version, "/"); i >= 0 {
			version = version[i+1:]
		}
		world.Resources = append(world.Resources, ownership.ServedResource{Group: gk.Group, Kind: gk.Kind, Version: version, Resource: strings.ToLower(gk.Kind) + "s", Namespaced: obj.Ref.Namespace != "", Verbs: []string{"get", "list", "watch", "delete"}})
	}
	session, err := ownershiptest.Scan(world).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func ownedObject(group, kind, namespace, name string, owner *ownershiptest.Object, controller bool) ownershiptest.Object {
	version := "v1"
	if group != "" {
		version = group + "/v1"
	}
	obj := ownershiptest.Object{Ref: resourceid.NewRef(group, kind, namespace, name), APIVersion: version, UID: types.UID(group + "/" + kind + "/" + namespace + "/" + name)}
	if owner != nil {
		obj.OwnerReferences = []metav1.OwnerReference{owner.OwnerRef(controller)}
	}
	return obj
}

func operatorApplication() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "monitoring", "namespace": "argocd"},
		"spec":     map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
		"status":   map[string]any{"resources": []any{map[string]any{"group": "monitoring.coreos.com", "kind": "Prometheus", "namespace": "monitoring", "name": "main", "status": "Synced"}}},
	}}
}

func TestGeneratedTreeUsesOwnerReferencesWithoutDisplayParent(t *testing.T) {
	for _, controller := range []bool{true, false} {
		t.Run(fmt.Sprintf("controller=%t", controller), func(t *testing.T) {
			prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
			sts := ownedObject("apps", "StatefulSet", "monitoring", "main", &prom, controller)
			pod := ownedObject("", "Pod", "monitoring", "main-0", &sts, true)
			getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
			for _, denied := range []string{"", "Prometheus", "StatefulSet", "Pod"} {
				t.Run("denied="+denied, func(t *testing.T) {
					got, _, err := newTestBuilder(t, getter, nil).WithOwnership(ownershipSession(t, prom, sts, pod)).WithReadCheck(func(ref ResourceRef) bool { return ref.Kind != denied }).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
					if err != nil {
						t.Fatal(err)
					}
					for _, obj := range []ownershiptest.Object{prom, sts, pod} {
						ref := ResourceRef{Group: obj.Ref.Group, Kind: obj.Ref.Kind, Namespace: obj.Ref.Namespace, Name: obj.Ref.Name}
						present := false
						for _, node := range got.Nodes {
							if node.ID == nodeID(ref) {
								present = true
							}
						}
						if present != (obj.Ref.Kind != denied) {
							t.Fatalf("denied %s: visibility of %s: %+v", denied, ref.Kind, got)
						}
					}
					for _, edge := range got.Edges {
						if denied == "StatefulSet" && edge.Target == nodeID(ResourceRef{Kind: "Pod", Namespace: "monitoring", Name: "main-0"}) {
							t.Fatalf("invented shortcut: %+v", edge)
						}
					}
					if denied != "" && len(got.Warnings) == 0 {
						t.Fatal("hidden node lacked warning")
					}
					if denied == "" && got.Summary.Generated != 2 {
						t.Fatalf("generated=%d", got.Summary.Generated)
					}
					if denied == "" && len(got.Edges) != 3 {
						t.Fatalf("want root → Prometheus → StatefulSet → Pod: %+v", got.Edges)
					}
				})
			}
		})
	}
}

func TestGeneratedTreeSeedsFetchedParentAndReportsColdChildren(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	sts := ownedObject("apps", "StatefulSet", "monitoring", "main", &prom, true)
	pod := ownedObject("", "Pod", "monitoring", "main-0", &sts, true)
	world := ownershiptest.World{
		Resources: []ownership.ServedResource{
			{Group: prom.Ref.Group, Version: "v1", Kind: "Prometheus", Resource: "prometheuses", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
			{Group: "apps", Version: "v1", Kind: "StatefulSet", Resource: "statefulsets", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
			{Version: "v1", Kind: "Pod", Resource: "pods", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
		},
		Objects: []ownershiptest.Object{prom, sts, pod},
		Collection: map[resourceid.GroupKind]ownershiptest.Collection{
			prom.Ref.GroupKind(): {State: ownershiptest.NotCollected},
			pod.Ref.GroupKind():  {State: ownershiptest.Loading},
		},
	}
	session, err := ownershiptest.Scan(world).Begin()
	if err != nil {
		t.Fatal(err)
	}
	fetched := &unstructured.Unstructured{}
	fetched.SetAPIVersion(prom.APIVersion)
	fetched.SetKind(prom.Ref.Kind)
	fetched.SetNamespace(prom.Ref.Namespace)
	fetched.SetName(prom.Ref.Name)
	fetched.SetUID(prom.UID)
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}):             operatorApplication(),
		refKey(ResourceRef{Group: prom.Ref.Group, Kind: prom.Ref.Kind, Namespace: prom.Ref.Namespace, Name: prom.Ref.Name}): fetched,
	}}
	got, _, err := NewBuilder(getter, nil).WithOwnership(session).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Generated != 1 || len(got.Warnings) == 0 {
		t.Fatalf("fetched owner or cold-child coverage lost: %+v", got)
	}
}

func TestGeneratedTreeRejectsWrongGroupAndNamespaceAndTerminatesCycle(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	sts := ownedObject("apps", "StatefulSet", "monitoring", "main", &prom, true)
	prom.OwnerReferences = []metav1.OwnerReference{sts.OwnerRef(true)}
	wrongGroup := ownedObject("", "Pod", "monitoring", "wrong-group", &prom, true)
	wrongGroup.OwnerReferences[0].APIVersion = "collision.example/v1"
	collision := ownedObject("collision.example", "Prometheus", "monitoring", "main", nil, false)
	outside := ownedObject("", "Pod", "outside", "wrong-namespace", &prom, true)
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
	session := &countingChildrenSession{ReadSession: ownershipSession(t, prom, sts, wrongGroup, collision, outside)}
	got, _, err := NewBuilder(getter, nil).WithOwnership(session).WithAllowedNamespaces([]string{"monitoring", "argocd"}).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Generated != 1 || session.calls != 2 {
		t.Fatalf("cycle or identity/scope mismatch: summary=%+v calls=%d", got.Summary, session.calls)
	}
}

func TestGeneratedTreeRejectsStaleTopologyOwner(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	sts := ownedObject("apps", "StatefulSet", "monitoring", "main", &prom, true)
	prom.UID = "replacement"
	topo := &topology.Topology{Nodes: []topology.Node{
		{ID: "prom", Kind: "Prometheus", Name: "main", Data: map[string]any{"namespace": "monitoring", "apiVersion": "monitoring.coreos.com/v1"}},
		{ID: "sts", Kind: topology.KindStatefulSet, Name: "main", Data: map[string]any{"namespace": "monitoring", "apiVersion": "apps/v1"}},
	}, Edges: []topology.Edge{{Source: "prom", Target: "sts", Type: topology.EdgeManages}}}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
	got, _, err := newTestBuilder(t, getter, topo).WithOwnership(ownershipSession(t, prom, sts)).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Generated != 0 {
		t.Fatalf("stale owner edge restored child: %+v", got)
	}
}

func TestGeneratedTreeFollowsNestedLocalInventory(t *testing.T) {
	for _, state := range []ownership.MembershipState{ownership.MembersRecorded, ownership.MembersRemote, ownership.MembersUnreadable} {
		t.Run(state.String(), func(t *testing.T) {
			childApp := ownedObject("argoproj.io", "Application", "monitoring", "child", nil, false)
			prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
			sts := ownedObject("apps", "StatefulSet", "monitoring", "main", &prom, true)
			childApp.Membership = state
			childApp.Members = []resourceid.Ref{prom.Ref}
			app := operatorApplication()
			unstructured.SetNestedSlice(app.Object, []any{map[string]any{"group": "argoproj.io", "kind": "Application", "namespace": "monitoring", "name": "child"}}, "status", "resources")
			getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): app}}
			got, _, err := NewBuilder(getter, nil).WithOwnership(ownershipSession(t, childApp, prom, sts)).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
			if err != nil {
				t.Fatal(err)
			}
			wantGenerated := 0
			if state == ownership.MembersRecorded {
				wantGenerated = 2
			}
			if got.Summary.Generated != wantGenerated {
				t.Fatalf("nested inventory: %+v", got)
			}
			if state == ownership.MembersRemote && !strings.Contains(strings.Join(got.Warnings, " "), "unconfirmed cluster") {
				t.Fatal("remote nested inventory lacked a warning")
			}
			if state == ownership.MembersUnreadable && !strings.Contains(strings.Join(got.Warnings, " "), "resource lists are unavailable") {
				t.Fatal("unreadable nested inventory lacked a warning")
			}
		})
	}
}

func TestGeneratedTreeRetainsNestedHelmClaimsOnlyWithoutLocalInventory(t *testing.T) {
	for _, state := range []ownership.MembershipState{ownership.MembersRecorded, ownership.MembersRemote, ownership.MembersUnreadable} {
		for _, currentClaim := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/claim=%t", state, currentClaim), func(t *testing.T) {
				hr := ownedObject("helm.toolkit.fluxcd.io", "HelmRelease", "monitoring", "child", nil, false)
				hr.Membership = state
				sts := ownedObject("apps", "StatefulSet", "monitoring", "main", nil, false)
				if currentClaim {
					sts.Labels = map[string]string{"helm.toolkit.fluxcd.io/name": "child", "helm.toolkit.fluxcd.io/namespace": "monitoring"}
				}
				app := operatorApplication()
				unstructured.SetNestedSlice(app.Object, []any{map[string]any{"group": hr.Ref.Group, "kind": hr.Ref.Kind, "namespace": hr.Ref.Namespace, "name": hr.Ref.Name}}, "status", "resources")
				getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): app}}
				topo := &topology.Topology{Nodes: []topology.Node{
					{ID: "hr", Kind: "HelmRelease", Name: "child", Data: map[string]any{"namespace": "monitoring", "apiVersion": hr.APIVersion}},
					{ID: "sts", Kind: topology.KindStatefulSet, Name: "main", Data: map[string]any{"namespace": "monitoring", "apiVersion": sts.APIVersion}},
				}, Edges: []topology.Edge{{Source: "hr", Target: "sts", Type: topology.EdgeManages}}}
				got, _, err := NewBuilder(getter, topo).WithOwnership(ownershipSession(t, hr, sts)).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if state == ownership.MembersUnreadable && currentClaim {
					want = 1
				}
				if got.Summary.Generated != want {
					t.Fatalf("nested Helm claim projection: %+v", got)
				}
			})
		}
	}

}

type invalidatedSession struct{ ownership.ReadSession }

func (invalidatedSession) ValidateCaches() error { return ownership.ErrCachesReplaced }

func TestGeneratedTreeRequiresCurrentSession(t *testing.T) {
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
	if _, _, err := NewBuilder(getter, nil).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io"); err == nil {
		t.Fatal("missing ownership dependency accepted")
	}
	_, _, err := newTestBuilder(t, getter, nil).WithOwnership(invalidatedSession{ownershipSession(t)}).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("invalidated session: %v", err)
	}
}

type countingChildrenSession struct {
	ownership.ReadSession
	calls int
}

type sessionWithGaps struct {
	ownership.ReadSession
	gaps []ownership.Gap
}

func (s sessionWithGaps) DependentCandidates(uid types.UID, scope ownership.SearchScope) ownership.DependentCandidatesRead {
	out := s.ReadSession.DependentCandidates(uid, scope)
	out.Gaps = append(out.Gaps, s.gaps...)
	return out
}

func TestGeneratedTreeDistinguishesCollectionScopeFromReadFailures(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	for _, tc := range []struct {
		name    string
		present bool
		gaps    []ownership.Gap
		warn    bool
	}{
		{name: "collected", present: true},
		{name: "absent"},
		{name: "uncollected", present: true, gaps: []ownership.Gap{{Kind: resourceid.GroupKind{Kind: "Lease", Group: "coordination.k8s.io"}, Namespace: "monitoring", Reason: readreason.RadarKindNotCollected}}},
		{name: "namespace scope", present: true, gaps: []ownership.Gap{{Kind: prom.Ref.GroupKind(), Namespace: "monitoring", Reason: readreason.RadarNamespaceNotCollected}}},
		{name: "partial discovery", present: true, gaps: []ownership.Gap{{Reason: readreason.KindUnknown}}, warn: true},
		{name: "loading", present: true, gaps: []ownership.Gap{{Kind: resourceid.GroupKind{Kind: "Pod"}, Namespace: "monitoring", Reason: readreason.KindSyncPending}}, warn: true},
		{name: "read failure", present: true, gaps: []ownership.Gap{{Kind: resourceid.GroupKind{Kind: "Pod"}, Namespace: "monitoring", Reason: readreason.ReadError}}, warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []ownershiptest.Object{}
			if tc.present {
				objects = append(objects, prom)
			} else {
				objects = append(objects, ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "another", nil, false))
			}
			session := sessionWithGaps{ReadSession: ownershipSession(t, objects...), gaps: tc.gaps}
			getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
			got, _, err := NewBuilder(getter, nil).WithOwnership(session).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
			if err != nil {
				t.Fatal(err)
			}
			if (len(got.Warnings) > 0) != tc.warn {
				t.Fatalf("warnings = %v, want warning=%t", got.Warnings, tc.warn)
			}
		})
	}
}

func TestGeneratedTreeKeepsPodsWithReplicaSetHistory(t *testing.T) {
	app := operatorApplication()
	var inventory []any
	var objects []ownershiptest.Object
	topo := &topology.Topology{}
	for i := range 100 {
		name := fmt.Sprintf("app-%03d", i)
		dep := ownedObject("apps", "Deployment", "monitoring", name, nil, false)
		inventory = append(inventory, map[string]any{"group": "apps", "kind": "Deployment", "namespace": "monitoring", "name": name})
		objects = append(objects, dep)
		for revision := range 11 {
			rs := ownedObject("apps", "ReplicaSet", "monitoring", fmt.Sprintf("%s-%02d", name, revision), &dep, true)
			objects = append(objects, rs)
			if revision == 10 {
				topo.Nodes = append(topo.Nodes, topology.Node{ID: name, Kind: topology.KindReplicaSet, Name: rs.Ref.Name, Data: map[string]any{"namespace": "monitoring", "apiVersion": "apps/v1"}})
				objects = append(objects, ownedObject("", "Pod", "monitoring", name, &rs, true))
			}
		}
	}
	unstructured.SetNestedSlice(app.Object, inventory, "status", "resources")
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): app}}
	got, _, err := NewBuilder(getter, topo).WithOwnership(ownershipSession(t, objects...)).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Declared != 100 || got.Summary.Generated != 200 || len(got.Warnings) != 0 {
		t.Fatalf("history crowded out current workloads: summary=%+v warnings=%v", got.Summary, got.Warnings)
	}
	for _, node := range got.Nodes {
		if node.Ref.Kind == "ReplicaSet" && !strings.HasSuffix(node.Ref.Name, "-10") {
			t.Fatalf("historical ReplicaSet shown: %+v", node.Ref)
		}
	}
}

func TestGeneratedTreeExpandsMoreThanOneThousandDeclaredRoots(t *testing.T) {
	app := operatorApplication()
	var inventory []any
	var objects []ownershiptest.Object
	for i := range 1001 {
		dep := ownedObject("apps", "Deployment", "monitoring", fmt.Sprintf("app-%04d", i), nil, false)
		inventory = append(inventory, map[string]any{"group": "apps", "kind": "Deployment", "namespace": "monitoring", "name": dep.Ref.Name})
		objects = append(objects, dep)
		if i == 1000 {
			objects = append(objects, ownedObject("", "Pod", "monitoring", "last-root-pod", &dep, true))
		}
	}
	unstructured.SetNestedSlice(app.Object, inventory, "status", "resources")
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): app}}
	got, _, err := NewBuilder(getter, nil).WithOwnership(ownershipSession(t, objects...)).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Declared != len(inventory) || got.Summary.Generated != 1 || len(got.Warnings) != 0 {
		t.Fatalf("last declared root not expanded: summary=%+v warnings=%v", got.Summary, got.Warnings)
	}
}

func (s *countingChildrenSession) DependentCandidates(uid types.UID, scope ownership.SearchScope) ownership.DependentCandidatesRead {
	s.calls++
	return s.ReadSession.DependentCandidates(uid, scope)
}

func TestGeneratedTreeBoundsAndCancellation(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)

	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancel), func(t *testing.T) {
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			if cancel {
				stop()
			}
			getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
			session := newFanoutSession(t, prom, maxExpansionSteps+10)
			got, _, err := newTestBuilder(t, getter, nil).WithOwnership(session).Build(ctx, "Application", "argocd", "monitoring", "argoproj.io")
			if cancel {
				if !errors.Is(err, context.Canceled) || got != nil || session.calls != 0 {
					t.Fatalf("cancelled build returned a result: %v err=%v calls=%d", got, err, session.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Summary.Declared != 1 || got.Summary.Generated >= maxExpansionSteps || session.calls > maxExpansionSteps || len(got.Warnings) == 0 {
				t.Fatalf("unbounded or lost declaration: summary=%+v calls=%d warnings=%v", got.Summary, session.calls, got.Warnings)
			}

		})
	}
}

func TestGeneratedTreeRemoteComparisonDoesNotReadLocalObjects(t *testing.T) {
	app := operatorApplication()
	unstructured.SetNestedField(app.Object, "https://remote.example", "status", "sync", "comparedTo", "destination", "server")
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): app}}
	session := &countingChildrenSession{ReadSession: ownershipSession(t)}
	got, _, err := newTestBuilder(t, getter, nil).WithOwnership(session).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got.Warnings, " "), "after Argo CD compares it here") {
		t.Fatal("waiting for comparison was not explained")
	}
	if !got.RemoteDestination || session.calls != 0 || len(getter.recordedCalls()) != 1 || got.Summary.Declared != 1 || got.Summary.Generated != 0 {
		t.Fatalf("remote comparison joined local data: %+v calls=%v", got, getter.recordedCalls())
	}
	for _, node := range got.Nodes {
		if node.Role == RoleDeclared && (!node.Remote || node.Ref.UID != "" || node.Health != "") {
			t.Fatalf("remote node enriched: %+v", node)
		}
	}
}

func TestGeneratedTreePreservesSpecGeneratorAndReportsUnsettledChain(t *testing.T) {
	for _, unsettled := range []bool{false, true} {
		t.Run(fmt.Sprintf("unsettled=%t", unsettled), func(t *testing.T) {
			prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
			sts := ownedObject("apps", "StatefulSet", "monitoring", "generated", nil, false)
			if unsettled {
				sts.OwnerReferences = []metav1.OwnerReference{{APIVersion: "missing.example/v1", Kind: "Unknown", Name: "unknown", UID: "unknown", Controller: func() *bool { v := true; return &v }()}}
			}
			topo := &topology.Topology{Nodes: []topology.Node{
				{ID: "prom", Kind: "Prometheus", Name: "main", Data: map[string]any{"namespace": "monitoring", "apiVersion": "monitoring.coreos.com/v1"}},
				{ID: "sts", Kind: topology.KindStatefulSet, Name: "generated", Data: map[string]any{"namespace": "monitoring", "apiVersion": "apps/v1"}},
			}, Edges: []topology.Edge{{Source: "prom", Target: "sts", Type: topology.EdgeManages}}}
			getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
			got, _, err := newTestBuilder(t, getter, topo).WithOwnership(ownershipSession(t, prom, sts)).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
			if err != nil {
				t.Fatal(err)
			}
			if unsettled {
				if got.Summary.Generated != 0 || len(got.Warnings) == 0 {
					t.Fatalf("unsettled graph edge silently accepted or omitted: %+v", got)
				}
			} else if got.Summary.Generated != 1 {
				t.Fatalf("spec generator lost: %+v", got)
			}
		})
	}
}

func TestGeneratedTreeDoesNotExposeUnrelatedTopologyWarnings(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
	got, _, err := NewBuilder(getter, &topology.Topology{Warnings: []string{"Failed to list unrelated objects: private details"}}).WithOwnership(ownershipSession(t, prom)).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("unrelated topology warning exposed: %v", got.Warnings)
	}
}

type declaredRootsSession struct {
	ownership.ReadSession
	calls int
}

func (s *declaredRootsSession) Object(ref resourceid.Ref) ownership.ObjectRead {
	return ownership.ObjectRead{State: ownership.Present, Object: ownership.Observation{Ref: ref, UID: types.UID(ref.Name)}}
}
func (s *declaredRootsSession) DependentCandidates(types.UID, ownership.SearchScope) ownership.DependentCandidatesRead {
	s.calls++
	return ownership.DependentCandidatesRead{}
}
func TestGeneratedTreeBoundsSearchesOfLargeDeclaredInventory(t *testing.T) {
	app := operatorApplication()
	var inventory []any
	for i := range maxExpansionSteps + 1 {
		inventory = append(inventory, map[string]any{"group": "apps", "kind": "Deployment", "namespace": "monitoring", "name": fmt.Sprintf("app-%05d", i)})
	}
	unstructured.SetNestedSlice(app.Object, inventory, "status", "resources")
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): app}}
	session := &declaredRootsSession{ReadSession: ownershipSession(t)}
	got, _, err := NewBuilder(getter, nil).WithOwnership(session).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if session.calls != maxExpansionSteps || got.Summary.Declared != len(inventory) || len(got.Warnings) == 0 {
		t.Fatalf("calls=%d summary=%+v warnings=%v", session.calls, got.Summary, got.Warnings)
	}
}

// The source returns indexed observations so large traversal tests do not make
// the independent scan oracle perform an unrelated quadratic scan.
type fanoutSession struct {
	ownership.ReadSession
	parent   types.UID
	children []ownership.Observation
	calls    int
	cancel   context.CancelFunc
}

func newFanoutSession(t *testing.T, parent ownershiptest.Object, count int) *fanoutSession {
	child := ownedObject("", "Pod", parent.Ref.Namespace, "child", &parent, true)
	s := &fanoutSession{ReadSession: ownershipSession(t, parent, child), parent: parent.UID}
	for i := range count {
		child := ownedObject("", "Pod", parent.Ref.Namespace, fmt.Sprintf("worker-%05d", i), &parent, true)
		s.children = append(s.children, ownership.Observation{Ref: child.Ref, UID: child.UID, OwnerReferences: child.OwnerReferences})
	}
	return s
}

func (s *fanoutSession) DependentCandidates(uid types.UID, scope ownership.SearchScope) ownership.DependentCandidatesRead {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	if uid == s.parent {
		return ownership.DependentCandidatesRead{Candidates: s.children}
	}
	return ownership.DependentCandidatesRead{}
}

func TestGeneratedTreeShowsMoreThanOneThousandDescendants(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	session := newFanoutSession(t, prom, 1500)
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
	got, _, err := NewBuilder(getter, nil).WithOwnership(session).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Generated != 1500 || len(got.Warnings) != 0 {
		t.Fatalf("summary=%+v warnings=%v", got.Summary, got.Warnings)
	}
}

func TestGeneratedTreeKeepsValidatedRetainedEdgeAtFrontier(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	session := newFanoutSession(t, prom, maxExpansionSteps+1)
	last := session.children[len(session.children)-1].Ref
	parent := ResourceRef{Group: prom.Ref.Group, Kind: prom.Ref.Kind, Namespace: prom.Ref.Namespace, Name: prom.Ref.Name}
	child := ResourceRef{Group: last.Group, Kind: last.Kind, Namespace: last.Namespace, Name: last.Name}
	nodes := map[string]Node{nodeID(parent): {Ref: parent}, nodeID(child): {Ref: child, Dependency: true}}
	edges := map[string]Edge{}
	b := NewBuilder(nil, nil)
	warnings, _ := b.addGenerated(context.Background(), ownership.NewView(session, traversalScope{}), []ResourceRef{parent}, nodes, edges, ToolArgoCD, nil, nil, false)
	if len(warnings) == 0 || nodes[nodeID(child)].Dependency || edges[edgeKey(nodeID(parent), nodeID(child))].Type != EdgeOwns {
		t.Fatalf("lost retained edge: warnings=%v child=%+v edge=%+v", warnings, nodes[nodeID(child)], edges[edgeKey(nodeID(parent), nodeID(child))])
	}
}

func TestGeneratedTreeCancellationDuringReadReturnsNoAnswer(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := newFanoutSession(t, prom, 10)
	session.cancel = cancel
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
	got, _, err := NewBuilder(getter, nil).WithOwnership(session).Build(ctx, "Application", "argocd", "monitoring", "argoproj.io")
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("cancelled build returned result=%v err=%v", got, err)
	}
}

func TestGeneratedTreeReportsPartialDiscovery(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	session, err := ownershiptest.Scan(ownershiptest.World{Partial: true, Objects: []ownershiptest.Object{prom}, Resources: []ownership.ServedResource{{Group: prom.Ref.Group, Kind: prom.Ref.Kind, Resource: "prometheuses", Version: "v1", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}}}).Begin()
	if err != nil {
		t.Fatal(err)
	}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
	got, _, err := NewBuilder(getter, nil).WithOwnership(session).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got.Warnings, " "), "could not be checked") {
		t.Fatalf("discovery failure lost: %v", got.Warnings)
	}
}

func TestGeneratedTreeExplainsHiddenRevisionParent(t *testing.T) {
	dep := ownedObject("apps", "Deployment", "monitoring", "main", nil, false)
	rs := ownedObject("apps", "ReplicaSet", "monitoring", "old", &dep, true)
	pod := ownedObject("", "Pod", "monitoring", "remaining", &rs, true)
	app := operatorApplication()
	unstructured.SetNestedSlice(app.Object, []any{map[string]any{"group": "apps", "kind": "Deployment", "namespace": "monitoring", "name": "main"}}, "status", "resources")
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): app}}
	got, _, err := NewBuilder(getter, &topology.Topology{}).WithOwnership(ownershipSession(t, dep, rs, pod)).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Generated != 1 || !strings.Contains(strings.Join(got.Warnings, " "), "inactive ReplicaSet parents") {
		t.Fatalf("hidden parent not explained: %+v", got)
	}
	for _, edge := range got.Edges {
		if strings.Contains(edge.Target, "/Pod/") {
			t.Fatalf("invented parent edge: %+v", edge)
		}
	}
}

func TestGeneratedTreeDisclosesFailedGraphUsedForSpecCandidates(t *testing.T) {
	prom := ownedObject("monitoring.coreos.com", "Prometheus", "monitoring", "main", nil, false)
	topo := &topology.Topology{Nodes: []topology.Node{{ID: "prom", Kind: "Prometheus", Name: "main", Data: map[string]any{"namespace": "monitoring", "apiVersion": prom.APIVersion}}}, Warnings: []string{"private list failure"}}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): operatorApplication()}}
	got, _, err := NewBuilder(getter, topo).WithOwnership(ownershipSession(t, prom)).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(got.Warnings, " ")
	if !strings.Contains(warnings, "graph data could not be loaded") || strings.Contains(warnings, "private") {
		t.Fatalf("warnings=%v", got.Warnings)
	}
}
