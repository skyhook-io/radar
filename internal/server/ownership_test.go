package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// withOwnershipDiscovery serves the fixture's kinds as Kubernetes does, with
// delete, so the garbage collector's kinds are the ones searched for
// dependents. It also marks the relationship graph for a rebuild on the next
// read, as Radar's first update cycle does, since a GET answers nothing
// without one.
func withOwnershipDiscovery(t *testing.T) {
	t.Helper()
	testServerSrv.broadcaster.markCachedTopologyDirty()
	verbs := metav1.Verbs{"create", "delete", "get", "list", "patch", "update", "watch"}
	fd := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fd.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: verbs},
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: verbs},
			{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: verbs},
			{Name: "services", Kind: "Service", Namespaced: true, Verbs: verbs},
			{Name: "limitranges", Kind: "LimitRange", Namespaced: true, Verbs: verbs},
			{Name: "namespaces", Kind: "Namespace", Verbs: verbs},
		}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
			{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: verbs},
			{Name: "replicasets", Kind: "ReplicaSet", Namespaced: true, Verbs: verbs},
		}},
	}
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
}

func relationshipsOf(t *testing.T, resp *http.Response) *topology.Relationships {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body struct {
		Relationships *topology.Relationships `json:"relationships"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Relationships == nil || body.Relationships.Ownership == nil {
		t.Fatalf("relationships %+v carry no ownership answer", body.Relationships)
	}
	return body.Relationships
}

func ownershipRefNames(refs []topology.ResourceRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.Kind+" "+r.Namespace+"/"+r.Name)
	}
	return out
}

func TestResourceGetAnswersOwnershipFromOwnerReferences(t *testing.T) {
	withOwnershipDiscovery(t)
	get := func(path string) *topology.Relationships {
		resp, err := http.Get(testServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return relationshipsOf(t, resp)
	}

	pod := get("/api/resources/pods/default/nginx-abc-xyz")
	if pod.Owner == nil || pod.Owner.Kind != "ReplicaSet" || pod.Owner.Name != "nginx-abc" || pod.Owner.Group != "apps" {
		t.Errorf("Pod owner = %+v, want its ReplicaSet", pod.Owner)
	}
	if pod.Deployment == nil || pod.Deployment.Name != "nginx" {
		t.Errorf("Pod deployment = %+v, want the ReplicaSet's Deployment", pod.Deployment)
	}

	rs := get("/api/resources/replicasets/default/nginx-abc?group=apps")
	if rs.Owner == nil || rs.Owner.Kind != "Deployment" || rs.Owner.Name != "nginx" {
		t.Errorf("ReplicaSet owner = %+v", rs.Owner)
	}
	if got := ownershipRefNames(rs.Children); len(got) != 1 || got[0] != "Pod default/nginx-abc-xyz" {
		t.Errorf("ReplicaSet children = %v, want its Pod", got)
	}
	if !rs.Ownership.Children.Complete || rs.Ownership.Children.Narrowed {
		t.Errorf("ReplicaSet children coverage = %+v; every kind is collected and readable", rs.Ownership.Children)
	}

	deploy := get("/api/resources/deployments/default/nginx?group=apps")
	if deploy.Owner != nil || deploy.Ownership.PrimaryOwnerState != "" {
		t.Errorf("Deployment owner = %+v (%q); it has no owner reference", deploy.Owner, deploy.Ownership.PrimaryOwnerState)
	}
	if got := ownershipRefNames(deploy.Children); len(got) != 1 || got[0] != "ReplicaSet default/nginx-abc" {
		t.Errorf("Deployment children = %v, want its ReplicaSet", got)
	}
}

// Until Radar has a relationship graph, as while it starts or after a context
// switch, a GET sends no relationships: the UI reads that as owners not mapped
// yet, and its GitOps write guard warns instead of finding no manager.
func TestResourceGetWithoutAGraphSendsNoRelationships(t *testing.T) {
	withOwnershipDiscovery(t)
	b := testServerSrv.broadcaster
	b.cachedTopologyMu.Lock()
	topo, idx, dirty := b.cachedTopology, b.cachedTopologyIndex, b.cachedTopologyDirty
	b.cachedTopology, b.cachedTopologyIndex, b.cachedTopologyDirty = nil, nil, false
	b.cachedTopologyMu.Unlock()
	t.Cleanup(func() {
		b.cachedTopologyMu.Lock()
		b.cachedTopology, b.cachedTopologyIndex, b.cachedTopologyDirty = topo, idx, dirty
		b.cachedTopologyMu.Unlock()
	})
	resp, err := http.Get(testServer.URL + "/api/resources/deployments/default/nginx?group=apps")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Relationships *topology.Relationships `json:"relationships"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || body.Relationships != nil {
		t.Fatalf("status %d, relationships %+v; want none without a graph", resp.StatusCode, body.Relationships)
	}
}

// A user who may read the namespace but not one of the kinds the GET gate
// checks there sees the same children, marked narrowed.
func TestResourceGetOwnershipForAUserWhoMayReadLess(t *testing.T) {
	withOwnershipDiscovery(t)
	env := newAuthTestServer(t)
	env.srv.broadcaster.markCachedTopologyDirty()
	for _, tc := range []struct {
		user     string
		secrets  bool
		narrowed bool
	}{
		{"alice", true, false},
		{"bob", false, true},
	} {
		env.srv.permCache.Set(tc.user, nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
		perms := env.srv.permCache.Get(tc.user, nil)
		perms.SetCanI("list", "", "secrets", "default", tc.secrets)
		perms.SetCanI("list", "", "limitranges", "default", true)
		perms.SetCanI("list", "*", "*", "", false)

		rel := relationshipsOf(t, env.authGet(t, "/api/resources/replicasets/default/nginx-abc?group=apps", tc.user, ""))
		if got := ownershipRefNames(rel.Children); len(got) != 1 || got[0] != "Pod default/nginx-abc-xyz" {
			t.Errorf("%s: children = %v, want the Pod", tc.user, got)
		}
		if rel.Ownership.Children.Narrowed != tc.narrowed {
			t.Errorf("%s: narrowed = %v, want %v", tc.user, rel.Ownership.Children.Narrowed, tc.narrowed)
		}
		if rel.Owner == nil || rel.Owner.Name != "nginx" {
			t.Errorf("%s: owner = %+v", tc.user, rel.Owner)
		}
	}
}

// Related Resources reads by the rules the per-user relationship graph
// applies, so ownership answers can't name what that graph strips: Calico
// policies and Secrets the user may not list in their namespace, kinds
// outside the user's namespaces, and cluster-scoped kinds the user may not
// list.
func TestRelationshipAccessFollowsTheGraphFilter(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}})
	perms := env.srv.permCache.Get("alice", nil)
	perms.SetCanI("list", "projectcalico.org", "networkpolicies", "alpha", false)
	perms.SetCanI("list", "crd.projectcalico.org", "networkpolicies", "alpha", true)
	perms.SetCanI("list", "", "secrets", "alpha", false)
	perms.SetCanI("list", "", "limitranges", "alpha", true)
	perms.SetCanI("list", "", "nodes", "", false)
	perms.SetCanI("list", "", "persistentvolumes", "", true)
	perms.SetCanI("list", "*", "*", "", false)
	gc := []string{"delete", "get", "list", "watch"}
	catalog := ownership.NewCatalog([]ownership.ServedResource{
		{Group: "projectcalico.org", Version: "v3", Kind: "NetworkPolicy", Resource: "networkpolicies", Namespaced: true, Verbs: gc},
		{Group: "crd.projectcalico.org", Version: "v1", Kind: "NetworkPolicy", Resource: "networkpolicies", Namespaced: true, Verbs: gc},
		{Group: "aggregated.io", Version: "v1", Kind: "Foo", Resource: "foos", Namespaced: true, Verbs: gc},
		{Group: "aggregated.io", Version: "v1", Kind: "Foo", Resource: "otherfoos", Namespaced: true, Verbs: gc},
	}, true)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice"}))
	a := env.srv.ownershipAccess(r, catalog)
	for _, tc := range []struct {
		kind      resourceid.GroupKind
		namespace string
		want      ownership.Decision
	}{
		{resourceid.GroupKind{Group: "projectcalico.org", Kind: "NetworkPolicy"}, "alpha", ownership.Denied},
		{resourceid.GroupKind{Group: "crd.projectcalico.org", Kind: "NetworkPolicy"}, "alpha", ownership.Allowed},
		{resourceid.GroupKind{Kind: "Secret"}, "alpha", ownership.Denied},
		{resourceid.GroupKind{Kind: "ConfigMap"}, "alpha", ownership.Allowed},
		{resourceid.GroupKind{Kind: "ConfigMap"}, "beta", ownership.Denied},
		{resourceid.GroupKind{Kind: "Node"}, "", ownership.Denied},
		{resourceid.GroupKind{Kind: "PersistentVolume"}, "", ownership.Allowed},
		{resourceid.GroupKind{Group: "example.io", Kind: "Unknown"}, "alpha", ownership.Denied},
		// No one grant decides a kind two resources serve.
		{resourceid.GroupKind{Group: "aggregated.io", Kind: "Foo"}, "alpha", ownership.Undecided},
		{resourceid.GroupKind{Group: "aggregated.io", Kind: "Foo"}, "", ownership.Undecided},
	} {
		if got := a.CanRead(tc.kind, tc.namespace); got != tc.want {
			t.Errorf("CanRead(%v, %q) = %v, want %v", tc.kind, tc.namespace, got, tc.want)
		}
	}
	if a.CanReadAllKindsIn("alpha") || a.CanReadEverything() {
		t.Errorf("CanReadAllKindsIn(alpha)=%v CanReadEverything=%v; Secrets and a Calico policy are hidden in alpha", a.CanReadAllKindsIn("alpha"), a.CanReadEverything())
	}
	// Every kind in a namespace includes each Calico policy kind there.
	for name, calico := range map[string]bool{"carol": false, "dave": true} {
		env.srv.permCache.Set(name, nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}})
		p := env.srv.permCache.Get(name, nil)
		p.SetCanI("list", "", "secrets", "alpha", true)
		p.SetCanI("list", "", "limitranges", "alpha", true)
		p.SetCanI("list", "projectcalico.org", "networkpolicies", "alpha", calico)
		p.SetCanI("list", "crd.projectcalico.org", "networkpolicies", "alpha", true)
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		got := env.srv.ownershipAccess(r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: name})), catalog).CanReadAllKindsIn("alpha")
		if got != calico {
			t.Errorf("%s: CanReadAllKindsIn(alpha) = %v with projectcalico.org policies readable %v", name, got, calico)
		}
	}
	if ns, all := a.Namespaces(); all || len(ns) != 1 || ns[0] != "alpha" {
		t.Errorf("Namespaces = %v %v", ns, all)
	}
	// A resource GET checks LimitRanges per namespace, so ownership does too.
	env.srv.permCache.Set("gina", nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}})
	gina := env.srv.permCache.Get("gina", nil)
	gina.SetCanI("list", "", "secrets", "alpha", true)
	gina.SetCanI("list", "", "limitranges", "alpha", false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	limited := env.srv.ownershipAccess(req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "gina"})), catalog)
	if got := limited.CanRead(resourceid.GroupKind{Kind: "LimitRange"}, "alpha"); got != ownership.Denied || limited.CanReadAllKindsIn("alpha") {
		t.Errorf("gina: CanRead(LimitRange, alpha) = %v, CanReadAllKindsIn(alpha) = %v; gina may not list LimitRanges there", got, limited.CanReadAllKindsIn("alpha"))
	}

	// In any namespace: an ordinary kind wherever namespace access reaches; a
	// kind checked per namespace by a cluster-wide grant, or else a grant in
	// one of the user's namespaces.
	user := func(name string, grants map[string]bool) ownership.Access {
		env.srv.permCache.Set(name, nil, &auth.UserPermissions{})
		perms := env.srv.permCache.Get(name, nil)
		perms.SetCanI("list", "", "secrets", "", false)
		perms.SetCanI("list", "", "limitranges", "", true)
		for _, ns := range allNamespaceNames() {
			perms.SetCanI("list", "", "secrets", ns, grants[ns])
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		return env.srv.ownershipAccess(r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: name})), catalog)
	}
	if len(allNamespaceNames()) < 2 {
		t.Fatalf("the fixture's namespaces = %v; want at least two", allNamespaceNames())
	}
	somewhere := user("bob", map[string]bool{allNamespaceNames()[0]: true})
	nowhere := user("erin", nil)
	unlisted := user("frank", nil)
	unlisted.(*requestAccess).clusterNamespaces = func() []string { return nil }
	for _, tc := range []struct {
		name string
		a    ownership.Access
		kind resourceid.GroupKind
		want ownership.Decision
	}{
		{"alice", a, resourceid.GroupKind{Kind: "ConfigMap"}, ownership.Allowed},
		{"alice", a, resourceid.GroupKind{Kind: "Secret"}, ownership.Denied},
		{"bob", somewhere, resourceid.GroupKind{Kind: "ConfigMap"}, ownership.Allowed},
		{"bob", somewhere, resourceid.GroupKind{Kind: "Secret"}, ownership.Allowed},
		{"bob", somewhere, resourceid.GroupKind{Kind: "LimitRange"}, ownership.Allowed},
		{"erin", nowhere, resourceid.GroupKind{Kind: "Secret"}, ownership.Denied},
		{"frank, without the cluster's namespaces", unlisted, resourceid.GroupKind{Kind: "Secret"}, ownership.Undecided},
	} {
		if got := tc.a.CanRead(tc.kind, ""); got != tc.want {
			t.Errorf("%s: Read(%v) in any namespace = %v, want %v", tc.name, tc.kind, got, tc.want)
		}
	}
}
