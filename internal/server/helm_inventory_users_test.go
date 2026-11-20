package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
)

func helmStamped(namespace, name, release string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: map[string]string{
		"meta.helm.sh/release-name":      release,
		"meta.helm.sh/release-namespace": namespace,
	}}
}

// Two people share one Radar and one cache, and neither can read Helm's
// storage. Each gets observed rows built only from what they can read: this
// goes through the real route, scope builder and storage resolution, which the
// helm package's own tests replace with hand-built scopes.
func TestHelmInventoryObservedRowsFollowEachCallersAccess(t *testing.T) {
	objects := append(testNamespaces("team-a", "team-b"),
		&corev1.ConfigMap{ObjectMeta: helmStamped("team-a", "cfg", "app-a")},
		&corev1.ConfigMap{ObjectMeta: helmStamped("team-b", "cfg", "app-b")},
		&rbacv1.Role{ObjectMeta: helmStamped("team-b", "reader", "app-b")},
		&rbacv1.Role{ObjectMeta: helmStamped("team-b", "only-role", "roles-only")},
	)
	useTestResourceCache(t, fake.NewClientset(objects...))
	useHelmStorageAnswering(t, http.StatusForbidden, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
	env := newAuthTestServer(t)

	alice := &auth.UserPermissions{AllowedNamespaces: []string{"team-a", "team-b"}}
	bob := &auth.UserPermissions{AllowedNamespaces: []string{"team-b"}}
	for _, perms := range []*auth.UserPermissions{alice, bob} {
		perms.SetCanI("list", "", "secrets", "", false)
		perms.SetCanI("list", "rbac.authorization.k8s.io", "roles", "", false)
	}
	alice.SetCanI("list", "rbac.authorization.k8s.io", "roles", "team-b", true)
	bob.SetCanI("list", "rbac.authorization.k8s.io", "roles", "team-b", false)
	env.srv.permCache.Set("alice", nil, alice)
	env.srv.permCache.Set("bob", nil, bob)

	read := func(user string) map[string]int {
		t.Helper()
		resp := env.authGet(t, "/api/helm/inventory", user, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", user, resp.StatusCode)
		}
		var inventory helm.ReleaseInventory
		if err := json.NewDecoder(resp.Body).Decode(&inventory); err != nil {
			t.Fatal(err)
		}
		if inventory.StorageDenied == nil || len(inventory.Releases) != 0 {
			t.Fatalf("%s: want every namespace's storage denied and no Helm rows, got %+v", user, inventory)
		}
		rows := map[string]int{}
		for _, row := range inventory.Observed {
			rows[row.Namespace+"/"+row.Name] = row.ResourceCount
		}
		return rows
	}

	if got, want := read("alice"), map[string]int{"team-a/app-a": 1, "team-b/app-b": 2, "team-b/roles-only": 1}; !maps.Equal(got, want) {
		t.Fatalf("alice = %v, want %v", got, want)
	}
	// Bob can't see team-a, and can't list Roles in team-b: no row or count
	// may come from either.
	if got, want := read("bob"), map[string]int{"team-b/app-b": 1}; !maps.Equal(got, want) {
		t.Fatalf("bob = %v, want %v", got, want)
	}
}
