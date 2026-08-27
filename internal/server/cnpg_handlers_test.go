package server

import (
	"os"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// A ClusterImageCatalog is cluster-scoped and referenceable from any namespace,
// which is why this lookup is an endpoint rather than a client-side list: asking
// the generic resources endpoint without a namespace inherits the caller's
// namespace view filter, and "no cluster uses this catalog" is exactly the
// sentence someone reads before editing it.

// CloudNativePG defaults an omitted `kind` to the namespaced ImageCatalog. A
// namespaced and a cluster-scoped catalog may share a name, so a reference that
// omits the kind must not be counted against the cluster-scoped one.
func TestCatalogRefMatches_DefaultsToTheNamespacedKind(t *testing.T) {
	for _, c := range []struct {
		name     string
		ref      map[string]interface{}
		catalog  string
		wantKind string
		want     bool
	}{
		{"omitted kind counts as ImageCatalog",
			map[string]interface{}{"name": "pg17"}, "pg17", "ImageCatalog", true},
		{"omitted kind is NOT a ClusterImageCatalog",
			map[string]interface{}{"name": "pg17"}, "pg17", "ClusterImageCatalog", false},
		{"explicit cluster-scoped matches its own kind",
			map[string]interface{}{"name": "pg17", "kind": "ClusterImageCatalog"}, "pg17", "ClusterImageCatalog", true},
		{"explicit cluster-scoped does not match the namespaced kind",
			map[string]interface{}{"name": "pg17", "kind": "ClusterImageCatalog"}, "pg17", "ImageCatalog", false},
		{"another catalog entirely",
			map[string]interface{}{"name": "pg16"}, "pg17", "ImageCatalog", false},
		{"no name at all",
			map[string]interface{}{}, "pg17", "ImageCatalog", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := catalogRefMatches(c.ref, c.catalog, c.wantKind); got != c.want {
				t.Errorf("catalogRefMatches(%v, %q, %q) = %v, want %v",
					c.ref, c.catalog, c.wantKind, got, c.want)
			}
		})
	}
}

// A caller who may not list Clusters is told so. Returning an empty list would
// read as "nothing depends on this catalog", which is the answer that gets a
// catalog edited out from under a running database.
func TestCNPGCatalogUsers_DeniesRatherThanReportingNoUsers(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
	allow(perms, cnpgGroup, "clusters", "", false)
	env.srv.permCache.Set("nobody", nil, perms)

	resp := env.authGet(t, "/api/cnpg/clusterimagecatalogs/postgres-fleet/clusters", "nobody", "")
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403 for a caller who may not list clusters", resp.StatusCode)
	}
}

// The namespaced route authorizes against that namespace, not cluster-wide.
func TestCNPGCatalogUsers_NamespacedRouteAuthorizesInItsNamespace(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
	allow(perms, cnpgGroup, "clusters", "pg", false)
	env.srv.permCache.Set("scoped", nil, perms)

	resp := env.authGet(t, "/api/cnpg/imagecatalogs/pg/postgres-pinned/clusters", "scoped", "")
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403 — the namespaced route must gate on its own namespace", resp.StatusCode)
	}
}

// The dynamic cache hands the same field back as int64 or float64 depending on
// how the object entered it. Missing the float64 shape would drop a real major
// to zero — which the screen reads as "the reference carries no major", a
// different and wrong statement.
func TestCatalogRefMajor_ReadsEitherNumberShape(t *testing.T) {
	for _, c := range []struct {
		name string
		ref  map[string]interface{}
		want int
	}{
		{"int64 from a typed decode", map[string]interface{}{"major": int64(17)}, 17},
		{"float64 from a JSON decode", map[string]interface{}{"major": float64(17)}, 17},
		{"plain int", map[string]interface{}{"major": 17}, 17},
		{"absent", map[string]interface{}{}, 0},
		{"a string is not a major", map[string]interface{}{"major": "17"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := catalogRefMajor(c.ref); got != c.want {
				t.Errorf("catalogRefMajor(%v) = %d, want %d", c.ref, got, c.want)
			}
		})
	}
}

// Same contract as the queued endpoint: an absent CRD is a real "nothing here",
// every other failure is a failure to look and must not read as "no cluster uses
// this catalog" — the sentence someone acts on before editing it.
func TestCNPGCatalogUsers_SeparatesAnAbsentCRDFromAFailedRead(t *testing.T) {
	src, err := os.ReadFile("cnpg_handlers.go")
	if err != nil {
		t.Fatalf("read handler: %v", err)
	}
	h := string(src)
	if !strings.Contains(h, "k8s.ErrUnknownDynamicKind") {
		t.Error("handler does not special-case an absent CRD, so every failure reads as no users")
	}
	if !strings.Contains(h, "StatusServiceUnavailable") {
		t.Error("handler has no failure path — a denied or unready read would answer 200 with no users")
	}
	if !strings.Contains(h, "listDynamicSynced") {
		t.Error("handler does not wait for the informer to sync; a cold read answers 'no users' before looking")
	}
}

// An empty result is only an absence if the cache actually looked. The informer
// for a namespace is started BY the read, so a gate that refuses to read until
// one exists never lets the first read happen — ListBlocking starts it and waits
// instead, which is what makes a subsequent empty list mean something.
func TestHandlersEstablishAbsenceRatherThanAssumeIt(t *testing.T) {
	src, err := os.ReadFile("policy_handlers.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func listDynamicSynced")
	if i < 0 {
		t.Fatal("helper moved")
	}
	fn := body[i : i+1400]
	if !strings.Contains(fn, "ListBlocking") {
		t.Error("the read does not wait for sync, so an empty list may be a cache that never looked")
	}
	// ListBlocking discards WaitForCacheSync's result: on timeout it returns an
	// empty list and no error, which is the false absence one layer down.
	if !strings.Contains(fn, "IsNamespaceSynced") {
		t.Error("nothing confirms the wait actually succeeded; a timed-out informer reads as an absence")
	}
	if !strings.Contains(fn, "errDynamicNotSynced") {
		t.Error("a cache that could not answer must say so, not return an empty result")
	}
	for _, f := range []string{"policy_handlers.go", "cnpg_handlers.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if !strings.Contains(string(b), "listDynamicSynced(") {
			t.Errorf("%s reads the cache without waiting for the scope it reads", f)
		}
		// A pre-read gate is the deadlock: nothing else starts the informer.
		if strings.Contains(string(b), "if !dynamicKindSynced(") {
			t.Errorf("%s gates on sync before reading; the first read of a namespace would never succeed", f)
		}
	}
}
