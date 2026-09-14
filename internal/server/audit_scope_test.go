package server

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

func TestAuditSecretAuthorizationAcrossSurfacesAndCachedScopes(t *testing.T) {
	env := newAuthTestServer(t)
	cache := k8s.GetResourceCache()
	user := "audit-reader"
	env.srv.permCache.Set(user, nil, &auth.UserPermissions{AllowedNamespaces: []string{"default", "kube-system"}})
	var priorAllowed *bp.ScanResults
	for _, allowed := range []bool{true, false, true} {
		perms := env.srv.permCache.Get(user, nil)
		perms.SetCanI("list", "", "secrets", "default", allowed)
		perms.SetCanI("list", "", "secrets", "kube-system", allowed)
		r := requestWithUser("GET", "/", &auth.User{Username: user})
		raw := env.authGet(t, "/api/audit?raw=true&namespaces=default,kube-system", user, "")
		if raw.StatusCode != http.StatusOK {
			t.Fatal(raw.StatusCode)
		}
		var result bp.ScanResults
		err := json.NewDecoder(raw.Body).Decode(&result)
		raw.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(result.MissingInputs, "secrets") == allowed {
			t.Fatal("Secret availability does not reflect resolved grant")
		}
		if !allowed {
			for _, f := range result.Findings {
				if f.Kind == "Secret" {
					t.Fatalf("hidden Secret: %+v", f)
				}
			}
		}
		if allowed {
			if priorAllowed != nil && !reflect.DeepEqual(result.CheckCounts, priorAllowed.CheckCounts) {
				t.Fatal("denied grant contaminated cached counts")
			}
			priorAllowed = &result
		} else if result.CheckCounts["orphanConfigMapSecret"].Evaluated >= priorAllowed.CheckCounts["orphanConfigMapSecret"].Evaluated {
			t.Fatal("denied Secrets still counted")
		}
		dashboard := env.srv.getDashboardAudit(r, cache, []string{"default", "kube-system"})
		if slices.Contains(dashboard.MissingInputs, "secrets") == allowed {
			t.Fatal("Home lost missing Secret inputs")
		}
		configured := applyAuditSettings(&result, getAuditConfig())
		if dashboard.Passing != configured.Summary.Passing || dashboard.Warning != configured.Summary.Warning || dashboard.Danger != configured.Summary.Danger {
			t.Fatal("Home audit diverged")
		}
		summary, rows := env.srv.computeAuditSummaryAndRows(r, cache, "", "Secret", "kube-system", "system-token")
		if allowed && (summary == nil || len(rows) == 0) {
			t.Fatal("authorized AI summary missing")
		}
		if !allowed && (summary == nil || summary.Count != 0 || !slices.Contains(summary.MissingInputs, "secrets") || len(rows) > 0) {
			t.Fatal("AI summary disclosed hidden Secret")
		}
		resource := env.authGet(t, "/api/audit/resource/secrets/kube-system/system-token?raw=true", user, "")
		var findings []bp.Finding
		err = json.NewDecoder(resource.Body).Decode(&findings)
		resource.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if allowed && len(findings) == 0 {
			t.Fatal("authorized resource audit missing")
		}
		if !allowed && len(findings) > 0 {
			t.Fatal("resource audit disclosed hidden Secret")
		}
	}
}

func TestAuditOptionsHonorsNamespaceSecretGrantsForGlobalViewer(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("viewer", nil, &auth.UserPermissions{})
	perms := env.srv.permCache.Get("viewer", nil)
	perms.SetCanI("list", "", "secrets", "", false)
	for _, ns := range k8s.AllNamespaceNames() {
		perms.SetCanI("list", "", "secrets", ns, ns == "default")
	}
	r := requestWithUser("GET", "/", &auth.User{Username: "viewer"})
	opts := env.srv.auditOptions(r)
	if opts.Scope.Namespaces != nil || !reflect.DeepEqual(opts.Scope.SecretNamespaces, []string{"default"}) {
		t.Fatalf("namespace Secret grant lost: %+v", opts.Scope)
	}
	for _, tc := range []struct {
		namespaces []string
		missing    bool
	}{
		{nil, true}, {[]string{"default", "kube-system"}, true}, {[]string{"default"}, false},
	} {
		results := getCachedResults(k8s.GetResourceCache(), tc.namespaces, opts)
		if slices.Contains(results.MissingInputs, "secrets") != tc.missing {
			t.Fatalf("partial Secret grant coverage for %v: %v", tc.namespaces, results.MissingInputs)
		}
	}
	for _, ns := range k8s.AllNamespaceNames() {
		perms.SetCanI("list", "", "secrets", ns, true)
	}
	allGranted := getCachedResults(k8s.GetResourceCache(), nil, env.srv.auditOptions(r))
	if slices.Contains(allGranted.MissingInputs, "secrets") {
		t.Fatal("namespace-local grants covering the complete namespace inventory were reported incomplete")
	}

}

func TestAuditCacheKeySeparatesAuthorizationAndResourceCache(t *testing.T) {
	cache := k8s.GetResourceCache()
	all := &audit.RunOptions{Scope: &audit.ReadScope{}}
	none := &audit.RunOptions{Scope: &audit.ReadScope{SecretNamespaces: []string{}}}
	if auditKey(cache, nil, all) == auditKey(cache, nil, none) {
		t.Fatal("all and no Secret grants collide")
	}
	if auditKey(cache, nil, all) == auditKey(&k8s.ResourceCache{}, nil, all) {
		t.Fatal("cache identities collide")
	}
	if auditKey(cache, []string{"a", "b"}, all) != auditKey(cache, []string{"b", "a"}, all) {
		t.Fatal("view ordering changes identity")
	}
	first := getCachedResults(cache, nil, all)
	if first != getCachedResults(cache, nil, all) {
		t.Fatal("same grant did not reuse HTTP memo")
	}
	denied := getCachedResults(cache, nil, none)
	if denied == first {
		t.Fatal("different grant reused HTTP memo")
	}
	if first == getCachedResults(cache, nil, all) {
		t.Fatal("single-slot memo unexpectedly retained prior profile")
	}
	(&Server{}).invalidatePostContextSwitchCaches()
	if auditCache.results != nil {
		t.Fatal("context invalidation retained result")
	}
}

func TestAuditHTTPClusterSubjectsRequireExactGrant(t *testing.T) {
	env := newAuthTestServer(t)
	gvr := schema.GroupVersionResource{Group: "database.example.com", Version: "v1", Resource: "databases"}
	mr := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "database.example.com/v1", "kind": "Database", "metadata": map[string]any{"name": "private-db"}, "spec": map[string]any{"providerConfigRef": map[string]any{"name": "default"}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": time.Now().Add(-time.Hour).Format(time.RFC3339)}}}}}
	outside := mr.DeepCopy()
	outside.SetName("outside")
	outside.SetNamespace("outside")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "DatabaseList"}, mr, outside)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Database", Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dynamic := k8s.GetDynamicResourceCache()
	if err := dynamic.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dynamic.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("sync timeout")
	}
	env.srv.permCache.Set("cluster-reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	perms := env.srv.permCache.Get("cluster-reader", nil)
	perms.SetCanI("list", "", "secrets", "default", false)
	for _, allowed := range []bool{true, false, true} {
		perms.SetCanI("list", gvr.Group, gvr.Resource, "", allowed)
		resp := env.authGet(t, "/api/audit?raw=true&namespace=default", "cluster-reader", "")
		var result bp.ScanResults
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := 0
		if allowed {
			want = 1
		}
		if got := result.CheckCounts["crossplaneStuck"].Evaluated; got != want {
			t.Fatalf("grant %v: counted %d, want %d", allowed, got, want)
		}
		found := false
		for _, f := range result.Findings {
			if f.Namespace == "outside" || (!allowed && f.Namespace == "") {
				t.Fatalf("unauthorized subject: %+v", f)
			}
			if f.CheckID == "crossplaneStuck" {
				found = true
			}
		}
		if found != allowed {
			t.Fatalf("grant %v: found %v", allowed, found)
		}
	}
	full := audit.RunFromCache(k8s.GetResourceCache(), nil, nil)
	scoped := audit.RunFromCache(k8s.GetResourceCache(), nil, &audit.RunOptions{Scope: audit.ResolveReadScope(nil, nil, func(string, string, string) bool { return true })})
	if !reflect.DeepEqual(full.CheckCounts, scoped.CheckCounts) || !reflect.DeepEqual(full.Summary, scoped.Summary) {
		t.Fatal("full dynamic access changed")
	}
}

func TestAuditHTTPGlobalViewerSecretSubjects(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("global-viewer", nil, &auth.UserPermissions{})
	perms := env.srv.permCache.Get("global-viewer", nil)
	perms.SetCanI("list", "", "secrets", "", false)
	baseline := 0
	for _, grant := range []bool{false, true} {
		for _, ns := range k8s.AllNamespaceNames() {
			perms.SetCanI("list", "", "secrets", ns, grant && ns == "default")
		}
		resp := env.authGet(t, "/api/audit?raw=true&namespaces=default,kube-system", "global-viewer", "")
		var result bp.ScanResults
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if !slices.Contains(result.MissingInputs, "secrets") {
			t.Fatal("partially readable Secrets must remain an incomplete input")
		}
		for _, f := range result.Findings {
			if f.Kind == "Secret" {
				if !grant || f.Namespace != "default" {
					t.Fatalf("hidden Secret: %+v", f)
				}
			}
		}
		if !grant {
			baseline = result.CheckCounts["orphanConfigMapSecret"].Evaluated
		} else if result.CheckCounts["orphanConfigMapSecret"].Evaluated != baseline+1 {
			t.Fatal("namespace Secret grant not counted")
		}
	}
}

func TestAuditCacheConcurrentScopes(t *testing.T) {
	cache := k8s.GetResourceCache()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(allowed bool) {
			defer wg.Done()
			var secrets []string
			if !allowed {
				secrets = []string{}
			}
			result := getCachedResults(cache, nil, &audit.RunOptions{Scope: &audit.ReadScope{SecretNamespaces: secrets}})
			if slices.Contains(result.MissingInputs, "secrets") == allowed {
				t.Error("concurrent scope contamination")
			}
			if !allowed {
				for _, f := range result.Findings {
					if f.Kind == "Secret" {
						t.Error("concurrent Secret disclosure")
					}
				}
			}
		}(i%2 == 0)
	}
	wg.Wait()
}
