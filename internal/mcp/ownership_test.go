package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestAgentMCPListBoundsSlowOwnershipAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name        string
		dynamic     bool
		contextMode string
		rows        int
	}{
		{name: "typed", rows: 80},
		{name: "dynamic", dynamic: true, rows: 80},
		{name: "typed_without_context", contextMode: "none", rows: 80},
		{name: "dynamic_without_context", dynamic: true, contextMode: "none", rows: 80},
		{name: "empty_dynamic", dynamic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "budget.example.test", Version: "v1", Resource: "widgets"}
			var typedObjects, dynamicObjects []runtime.Object
			for i := range tc.rows {
				ns := fmt.Sprintf("budget-%03d", i)
				typedObjects = append(typedObjects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
				if tc.dynamic {
					obj := &unstructured.Unstructured{Object: map[string]any{
						"apiVersion": gvr.GroupVersion().String(), "kind": "Widget",
						"metadata": map[string]any{"namespace": ns, "name": "row", "uid": ns},
						"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False"}}},
					}}
					dynamicObjects = append(dynamicObjects, obj)
				} else {
					typedObjects = append(typedObjects, &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "row", UID: types.UID(ns)},
						Status:     corev1.PodStatus{Phase: corev1.PodFailed},
					})
				}
			}
			if err := k8s.InitTestResourceCache(fake.NewClientset(typedObjects...)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "budget"})
			if err := k8s.InitTestDynamicResourceCache(
				dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, dynamicObjects...),
				[]k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}},
			); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			if _, err := k8s.GetResourceCache().ListDynamicWithGroup(context.Background(), "Widget", "", gvr.Group); err != nil {
				t.Fatal(err)
			}
			if !k8s.GetDynamicResourceCache().WaitForSync(gvr, 5*time.Second) {
				t.Fatal("dynamic cache did not sync")
			}
			previousClient := k8s.SetTestClient(&kubernetes.Clientset{})
			t.Cleanup(func() { k8s.SetTestClient(previousClient) })
			relationshipTopology.RLock()
			previousSource := relationshipTopology.source
			relationshipTopology.RUnlock()
			graph := &topology.Topology{}
			index := topology.IndexByResource(graph)
			SetRelationshipTopologySource(func() (*topology.Topology, *topology.RelationshipsIndex) { return graph, index })
			t.Cleanup(func() { SetRelationshipTopologySource(previousSource) })
			ctx, cancel := context.WithTimeout(withTestUserPerms(t, "budget-"+tc.name, nil, nil), 15*time.Second)
			defer cancel()
			resource := "pods"
			input := listResourcesInput{Kind: "pods", Context: tc.contextMode}
			if tc.dynamic {
				resource = gvr.Resource
				input = listResourcesInput{Kind: "Widget", Group: gvr.Group, Context: tc.contextMode}
			}
			var gets atomic.Int32
			stubSubjectCanI(t, func(ctx context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, _ string, checkedResource, verb string) (bool, error) {
				if namespace != "" && checkedResource == resource && verb == "get" {
					gets.Add(1)
					timer := time.NewTimer(100 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-ctx.Done():
						return false, ctx.Err()
					}
				}
				return true, nil
			})
			started := time.Now()
			result, _, err := handleListResources(ctx, nil, input)
			elapsed := time.Since(started)
			if err != nil {
				t.Fatalf("actual list_resources route failed after %s: %v", elapsed, err)
			}
			rows, _ := completeListBody(t, result)
			if len(rows) != tc.rows {
				t.Fatalf("got %d rows, want %d", len(rows), tc.rows)
			}
			budgetNotes := 0
			for _, row := range rows {
				encoded, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				var decoded struct {
					Summary *resourcecontext.ResourceSummaryContext `json:"summaryContext"`
				}
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				if tc.contextMode == "none" {
					if decoded.Summary != nil {
						t.Fatalf("context=none emitted summary: %s", encoded)
					}
					continue
				}
				if decoded.Summary == nil || decoded.Summary.Health == "" {
					t.Fatalf("health lost: %s", encoded)
				}
				for _, omission := range decoded.Summary.Omitted {
					if omission.Field == "managedBy" && omission.Reason == resourcecontext.OmittedBudgetExceeded {
						budgetNotes++
					}
				}
			}
			t.Logf("route=%s rows=%d elapsed=%s exact_get_checks=%d budget_omissions=%d", tc.name, len(rows), elapsed, gets.Load(), budgetNotes)
			if tc.contextMode == "none" || tc.rows == 0 {
				if gets.Load() != 0 {
					t.Fatalf("unrequested enrichment performed %d GET checks", gets.Load())
				}
				return
			}
			if gets.Load() >= 60 || budgetNotes == 0 {
				t.Errorf("list enrichment escaped its 3-second budget: elapsed=%s checks=%d budget_omissions=%d", elapsed, gets.Load(), budgetNotes)
			}
		})
	}
}

func TestAgentOwnershipMCPListAndGetPreferController(t *testing.T) {
	yes := true
	replicas := int32(1)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "web", UID: "uid-dep"}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx"}}}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "web-a", UID: "uid-pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "uid-dep", Controller: &yes}}, Labels: map[string]string{"argocd.argoproj.io/instance": "copied"}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(deployment, pod, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "ownership-test"})
	verbs := metav1.Verbs{"get", "list", "watch", "delete"}
	fd := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fd.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: verbs}}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: verbs}}},
	}
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "pods", Namespace: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := completeListBody(t, result)
	if len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		Summary *resourcecontext.ResourceSummaryContext `json:"summaryContext"`
	}
	if err := json.Unmarshal(encoded, &row); err != nil {
		t.Fatal(err)
	}
	if row.Summary == nil || row.Summary.ManagedBy == nil || row.Summary.ManagedBy.Kind != "Deployment" {
		t.Fatalf("summary %+v", row.Summary)
	}
	result, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "pods", Namespace: "alpha", Name: "web-a"})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Context *resourcecontext.ResourceContext `json:"resourceContext"`
	}
	if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
		t.Fatal(err)
	}
	if body.Context == nil || len(body.Context.ManagedBy) != 1 || body.Context.ManagedBy[0].Kind != "Deployment" {
		t.Fatalf("context %+v", body.Context)
	}
}

// A namespaced kind in any namespace: a cluster-wide grant allows it, and
// anything else is undecided rather than checked namespace by namespace.
func TestOwnershipAccessDecidesAKindInAnyNamespace(t *testing.T) {
	t.Cleanup(func() { getPermCache().Invalidate() })
	secret := resourceid.GroupKind{Kind: "Secret"}
	for name, clusterWide := range map[string]bool{"everywhere": true, "some namespaces": false} {
		perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta"}}
		perms.SetCanI("get", "", "secrets", "", clusterWide)
		perms.SetCanI("get", "", "secrets", "alpha", false)
		perms.SetCanI("get", "", "secrets", "beta", true)
		getPermCache().Set(name, nil, perms)
		ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: name})
		a := ownershipAccess(ctx, ownership.NewCatalog(nil, true))
		want := ownership.Undecided
		if clusterWide {
			want = ownership.Allowed
		}
		if got := a.CanRead(secret, ""); got != want {
			t.Errorf("%s: Read(Secret) in any namespace = %v, want %v", name, got, want)
		}
		if got := a.CanRead(secret, "alpha"); got != ownership.Denied {
			t.Errorf("%s: Read(Secret, alpha) = %v, want denied", name, got)
		}
	}
}

// A resource context names only an object's owner, so building one asks no
// permission question about the kinds the object's children could be: a Node
// on a cluster serving many kinds Radar doesn't collect, for a user who may
// read every namespace but none of those kinds.
func TestResourceContextOwnerAsksNothingAboutChildren(t *testing.T) {
	defer k8s.ResetTestState()
	verbs := metav1.Verbs{"create", "delete", "get", "list", "patch", "update", "watch"}
	things := &metav1.APIResourceList{GroupVersion: "things.example.io/v1"}
	for i := range 20 {
		things.APIResources = append(things.APIResources, metav1.APIResource{Name: fmt.Sprint("things", i), Kind: fmt.Sprint("Thing", i), Namespaced: true, Verbs: verbs})
	}
	fd := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fd.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: verbs},
			{Name: "nodes", Kind: "Node", Verbs: verbs},
			{Name: "namespaces", Kind: "Namespace", Verbs: verbs},
		}},
		things,
	}
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	node := &corev1.Node{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: "n1", UID: "uid-n1"}}
	objects := []runtime.Object{node}
	for i := range 10 {
		objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprint("ns-", i)}})
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	prev := k8s.SetTestClient(&kubernetes.Clientset{})
	defer k8s.SetTestClient(prev)
	ctx := withTestUserPerms(t, "viewer", nil, nil)
	var asked atomic.Int64
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		if group == "things.example.io" {
			asked.Add(1)
		}
		return group == "" && resource == "nodes", nil
	})

	rc := buildMCPResourceContext(ctx, node, "Node", "", "n1", resourcecontext.TierBasic, nil)
	if rc == nil || rc.Owner != nil {
		t.Fatalf("context = %+v; want one without an owner", rc)
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("building the context asked %d permission questions about kinds the Node's children could be", n)
	}
}

// A kind two resources of one group serve can't be searched for children,
// and no one grant decides whether the caller may read it, so the answer
// keeps the gap rather than claiming to be complete.
func TestOwnershipAccessKeepsAGapOnAnAmbiguousKind(t *testing.T) {
	t.Cleanup(func() { getPermCache().Invalidate() })
	perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"alpha"}}
	perms.SetCanI("get", "apps", "deployments", "alpha", true)
	getPermCache().Set("hana", nil, perms)
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "hana"})
	b := ownershiptest.NewBuilder()
	owner := b.Add("apps", "Deployment", "alpha", "parent")
	w := b.World()
	for _, plural := range []string{"foos", "otherfoos"} {
		w.Resources = append(w.Resources, ownership.ServedResource{Group: "aggregated.io", Version: "v1", Kind: "Foo", Resource: plural, Namespaced: true, Verbs: []string{"delete", "list", "watch"}})
	}
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	foo := resourceid.GroupKind{Group: "aggregated.io", Kind: "Foo"}
	a := ownershipAccess(ctx, s.Catalog())
	if got := a.CanRead(foo, "alpha"); got != ownership.Undecided {
		t.Fatalf("CanRead(Foo, alpha) = %v, want undecided", got)
	}
	got := ownership.NewView(s, a).Children(owner.Ref)
	if got.Complete() || !slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == foo }) {
		t.Fatalf("children = %+v; want the ambiguous kind left unread", got)
	}
}

// A recorded member of a kind no served resource names, as when its CRD was
// removed, is one Radar can't check, not one the user may not see.
func TestOwnershipAccessCountsAnUnservedMemberAsUnknown(t *testing.T) {
	t.Cleanup(func() { getPermCache().Invalidate() })
	perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"alpha"}}
	perms.SetCanI("get", "argoproj.io", "applications", "alpha", true)
	perms.SetCanI("get", "", "configmaps", "alpha", true)
	getPermCache().Set("ivo", nil, perms)
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "ivo"})
	b := ownershiptest.NewBuilder()
	cm := b.Add("", "ConfigMap", "alpha", "settings")
	gone := resourceid.Ref{Group: "gone.example.io", Kind: "Widget", Namespace: "alpha", Name: "w"}
	app := b.Manager(ownership.ArgoApplication, "alpha", "shop", ownership.MembersRecorded, "alpha", cm.Ref, gone)
	s, err := ownershiptest.Scan(b.World()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	got := ownership.NewView(s, ownershipAccess(ctx, s.Catalog())).Members(app.Ref)
	if got.Withheld != 0 || got.Unknown != 1 || len(got.Members) != 1 {
		t.Fatalf("members = %d listed, %d withheld, %d unknown; want 1, 0, 1", len(got.Members), got.Withheld, got.Unknown)
	}
	if !slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == gone.GroupKind() && g.Reason == readreason.KindUnknown }) {
		t.Fatalf("gaps = %+v; want the unserved kind named as unknown", got.Gaps)
	}
}
