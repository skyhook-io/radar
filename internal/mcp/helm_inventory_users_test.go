package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// list_helm_releases for two callers on one cache, neither able to read Helm's
// storage: each gets observed rows from what they can read, through the real
// tool, scope builder and storage resolution.
func TestListHelmReleasesObservedRowsFollowEachCallersAccess(t *testing.T) {
	stamped := func(namespace, name, release string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: map[string]string{
			"meta.helm.sh/release-name":      release,
			"meta.helm.sh/release-namespace": namespace,
		}}
	}
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
		&corev1.ConfigMap{ObjectMeta: stamped("team-a", "cfg", "app-a")},
		&corev1.ConfigMap{ObjectMeta: stamped("team-b", "cfg", "app-b")},
		&rbacv1.Role{ObjectMeta: stamped("team-b", "reader", "app-b")},
		&rbacv1.Role{ObjectMeta: stamped("team-b", "only-role", "roles-only")},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	discovered := make(chan struct{})
	dc := k8s.GetDynamicResourceCache()
	dc.OnCRDDiscoveryComplete(func() { close(discovered) })
	dc.DiscoverAllCRDs()
	select {
	case <-discovered:
	case <-time.After(5 * time.Second):
		t.Fatal("empty CRD discovery did not complete")
	}

	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = io.WriteString(w, `{"major":"1","minor":"33","gitVersion":"v1.33.0"}`)
			return
		}
		if !strings.Contains(r.URL.Path, "/secrets") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
	}))
	t.Cleanup(storage.Close)
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(&rest.Config{Host: storage.URL}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(helm.ResetClient)

	alice := &pkgauth.UserPermissions{AllowedNamespaces: []string{"team-a", "team-b"}}
	bob := &pkgauth.UserPermissions{AllowedNamespaces: []string{"team-b"}}
	for _, perms := range []*pkgauth.UserPermissions{alice, bob} {
		perms.SetCanI("list", "", "secrets", "", false)
		perms.SetCanI("list", "rbac.authorization.k8s.io", "roles", "", false)
	}
	alice.SetCanI("list", "rbac.authorization.k8s.io", "roles", "team-b", true)
	bob.SetCanI("list", "rbac.authorization.k8s.io", "roles", "team-b", false)
	getPermCache().Set("alice", nil, alice)
	getPermCache().Set("bob", nil, bob)
	t.Cleanup(getPermCache().Invalidate)

	read := func(user string) map[string]int {
		t.Helper()
		ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: user})
		result, _, err := handleListHelmReleases(ctx, nil, listHelmReleasesInput{})
		if err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		var wire struct {
			Releases []helm.HelmRelease     `json:"releases"`
			Observed []helm.ObservedRelease `json:"observed"`
		}
		if err := json.Unmarshal([]byte(extractText(t, result)), &wire); err != nil {
			t.Fatalf("%s: decode: %v", user, err)
		}
		if len(wire.Releases) != 0 {
			t.Fatalf("%s: want no Helm rows, got %+v", user, wire.Releases)
		}
		rows := map[string]int{}
		for _, row := range wire.Observed {
			rows[row.Namespace+"/"+row.Name] = row.ResourceCount
		}
		return rows
	}

	if got, want := read("alice"), map[string]int{"team-a/app-a": 1, "team-b/app-b": 2, "team-b/roles-only": 1}; !maps.Equal(got, want) {
		t.Fatalf("alice = %v, want %v", got, want)
	}
	if got, want := read("bob"), map[string]int{"team-b/app-b": 1}; !maps.Equal(got, want) {
		t.Fatalf("bob = %v, want %v", got, want)
	}

	get := func(user, namespace, name, include string) (map[string]any, error) {
		t.Helper()
		ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: user})
		result, _, err := handleGetHelmRelease(ctx, nil, getHelmReleaseInput{Namespace: namespace, Name: name, Include: include})
		if err != nil {
			return nil, err
		}
		var wire map[string]any
		if err := json.Unmarshal([]byte(extractText(t, result)), &wire); err != nil {
			t.Fatalf("%s: decode: %v", user, err)
		}
		return wire, nil
	}
	resourceCount := func(t *testing.T, wire map[string]any) float64 {
		t.Helper()
		observed, ok := wire["observed"].(map[string]any)
		if !ok {
			t.Fatalf("no observed release in %v", wire)
		}
		if _, ok := wire["status"]; ok {
			t.Fatalf("an observed release must not carry a status: %v", wire)
		}
		if summary, _ := wire["summary"].(string); !strings.Contains(summary, "don't assume it is deployed or healthy") {
			t.Fatalf("summary = %q", summary)
		}
		return observed["resourceCount"].(float64)
	}

	t.Run("get_helm_release falls back to each caller's observed release", func(t *testing.T) {
		wire, err := get("alice", "team-b", "app-b", "")
		if err != nil {
			t.Fatal(err)
		}
		if got := resourceCount(t, wire); got != 2 {
			t.Fatalf("alice sees %v objects of app-b, want 2", got)
		}
		wire, err = get("bob", "team-b", "app-b", "values,history,resource_diff")
		if err != nil {
			t.Fatal(err)
		}
		if got := resourceCount(t, wire); got != 1 {
			t.Fatalf("bob sees %v objects of app-b, want 1 (his Role read is denied)", got)
		}
		if got := fmt.Sprint(wire["unavailable"]); got != "[values history resource_diff]" {
			t.Fatalf("unavailable = %s", got)
		}
	})

	// The fixture's cache never finishes syncing every kind, so the reason here
	// is "try again"; the settled "none belong to it" case is a helm unit test.
	t.Run("get_helm_release stays a denial when the caller can read none of the release", func(t *testing.T) {
		_, err := get("bob", "team-b", "roles-only", "")
		if err == nil || !strings.Contains(err.Error(), "access to its Secrets is denied, which doesn't mean it's missing") {
			t.Fatalf("err = %v", err)
		}
	})
}
