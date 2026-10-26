package mcp

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakediscovery "k8s.io/client-go/discovery/fake"
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
)

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
