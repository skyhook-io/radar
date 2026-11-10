package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The shared TestMain fixture holds Deployment nginx in "default" and
// Deployment stuck-app in "broken"; the issues engine reports stuck-app plus
// two "default" problems.

// namespacesOf GETs path as user and returns the sorted, de-duplicated
// namespaces that the response's items carry, located by pick.
func namespacesOf(t *testing.T, env *authTestEnv, path, user string, pick func(body []byte) []string) []string {
	t.Helper()
	resp := env.authGet(t, path, user, "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s as %s: status %d", path, user, resp.StatusCode)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	seen := map[string]bool{}
	var out []string
	for _, ns := range pick(raw) {
		if ns != "" && !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	slices.Sort(out)
	return out
}

func decodeInto[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

// A pick made in the UI must not narrow a read that names no namespaces — the
// read Radar Hub's fan-outs make as the same user. Each of these surfaces
// answers for the caller's whole RBAC ceiling; an explicit filter still
// narrows, RBAC still bounds, and the preference read still echoes the pick
// for the UI.
func TestProxyAuth_PlainReadsIgnoreSavedPick(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	seedTimelineAcrossNamespaces(t)

	env := newAuthTestServer(t)
	for _, user := range []string{"alice", "carol"} {
		perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"default", "broken"}}
		if user == "carol" {
			perms = &pkgauth.UserPermissions{AllowedNamespaces: []string{"broken"}}
		}
		for _, ns := range perms.AllowedNamespaces {
			perms.SetCanI("list", "apps", "deployments", ns, true)
		}
		env.srv.permCache.Set(user, nil, perms)
		// The pick goes through the UI's own POST, so this is the state a
		// browser session leaves behind.
		resp := env.authPost(t, "/api/cluster/namespace", user, "", `{"namespaces":["broken"]}`)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST pick as %s: status %d", user, resp.StatusCode)
		}
	}

	type nsItem struct {
		Namespace string `json:"namespace"`
		Metadata  struct {
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Data struct {
			Namespace string `json:"namespace"`
		} `json:"data"`
	}
	itemNamespaces := func(items []nsItem) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Namespace, it.Metadata.Namespace, it.Data.Namespace)
		}
		return out
	}
	list := func(body []byte) []string { return itemNamespaces(decodeInto[[]nsItem](t, body)) }
	resources := func(body []byte) []string {
		return itemNamespaces(decodeInto[struct {
			Items []nsItem `json:"items"`
		}](t, body).Items)
	}
	topology := func(body []byte) []string {
		return itemNamespaces(decodeInto[struct {
			Nodes []nsItem `json:"nodes"`
		}](t, body).Nodes)
	}
	issues := func(body []byte) []string {
		return itemNamespaces(decodeInto[struct {
			Issues []nsItem `json:"issues"`
		}](t, body).Issues)
	}
	search := func(body []byte) []string {
		return itemNamespaces(decodeInto[struct {
			Hits []nsItem `json:"hits"`
		}](t, body).Hits)
	}
	dashboard := func(body []byte) []string {
		return itemNamespaces(decodeInto[struct {
			Problems []nsItem `json:"problems"`
		}](t, body).Problems)
	}
	both := []string{"broken", "default"}
	onlyBroken := []string{"broken"}
	onlyDefault := []string{"default"}

	cases := []struct {
		user, path string
		pick       func([]byte) []string
		want       []string
	}{
		{"alice", "/api/resources/deployments", resources, both},
		{"alice", "/api/topology", topology, both},
		{"alice", "/api/issues", issues, both},
		{"alice", "/api/changes", list, both},
		{"alice", "/api/search?q=kind:Deployment", search, both},
		{"alice", "/api/dashboard", dashboard, both},
		// An explicit filter still narrows.
		{"alice", "/api/resources/deployments?namespaces=default", resources, onlyDefault},
		{"alice", "/api/topology?namespaces=default", topology, onlyDefault},
		{"alice", "/api/issues?namespaces=default", issues, onlyDefault},
		{"alice", "/api/changes?namespaces=default", list, onlyDefault},
		{"alice", "/api/search?q=kind:Deployment&namespaces=default", search, onlyDefault},
		// RBAC still bounds a plain read.
		{"carol", "/api/resources/deployments", resources, onlyBroken},
		{"carol", "/api/issues", issues, onlyBroken},
		{"carol", "/api/search?q=kind:Deployment", search, onlyBroken},
	}
	for _, tc := range cases {
		if got := namespacesOf(t, env, tc.path, tc.user, tc.pick); !slices.Equal(got, tc.want) {
			t.Errorf("GET %s as %s: namespaces %v, want %v", tc.path, tc.user, got, tc.want)
		}
	}

	deployments := func(path string) int {
		t.Helper()
		resp := env.authGet(t, path, "alice", "")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, resp.StatusCode)
		}
		var body struct {
			Counts map[string]int `json:"counts"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return body.Counts["apps/Deployment"]
	}
	if got := deployments("/api/resource-counts"); got != 2 {
		t.Errorf("resource-counts Deployments = %d, want 2 (both namespaces)", got)
	}
	if got := deployments("/api/resource-counts?namespaces=broken"); got != 1 {
		t.Errorf("resource-counts Deployments with an explicit filter = %d, want 1", got)
	}

	// The preference read still echoes the pick, so the UI keeps its selection.
	resp := env.authGet(t, "/api/cluster/namespace-scope", "alice", "")
	defer func() { _ = resp.Body.Close() }()
	var scope NamespaceScopeResponse
	if err := json.NewDecoder(resp.Body).Decode(&scope); err != nil {
		t.Fatalf("decode namespace-scope: %v", err)
	}
	if !slices.Equal(scope.Actives, []string{"broken"}) || scope.Mode != NamespaceScopeNamespace {
		t.Errorf("namespace-scope = actives %v mode %q, want [broken] %q", scope.Actives, scope.Mode, NamespaceScopeNamespace)
	}
}

// --namespaces seeds each user's initial UI selection; it must not narrow a
// background poller's read (Radar Hub's alerts worker polls /api/issues as a
// system identity and names no namespace).
func TestProxyAuth_ConfiguredNamespacesDoNotNarrowSystemReads(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	k8s.SetFallbackNamespaces([]string{"default"})
	t.Cleanup(func() { k8s.SetFallbackNamespace("") })

	env := newAuthTestServer(t)
	const system = "radar:system:alerts:test"
	env.srv.permCache.Set(system, nil, &pkgauth.UserPermissions{AllowedNamespaces: nil})
	env.srv.permCache.Set("alice", nil, &pkgauth.UserPermissions{AllowedNamespaces: nil})

	issues := func(body []byte) []string {
		var out []string
		for _, is := range decodeInto[struct {
			Issues []struct {
				Namespace string `json:"namespace"`
			} `json:"issues"`
		}](t, body).Issues {
			out = append(out, is.Namespace)
		}
		return out
	}
	for range 2 { // the second read would see a seed the first one planted
		if got := namespacesOf(t, env, "/api/issues", system, issues); !slices.Equal(got, []string{"broken", "default"}) {
			t.Fatalf("system issues namespaces = %v, want [broken default]", got)
		}
	}

	// A human user's UI still starts on the configured list.
	resp := env.authGet(t, "/api/cluster/namespace-scope", "alice", "")
	defer func() { _ = resp.Body.Close() }()
	var scope NamespaceScopeResponse
	if err := json.NewDecoder(resp.Body).Decode(&scope); err != nil {
		t.Fatalf("decode namespace-scope: %v", err)
	}
	if !slices.Equal(scope.Actives, []string{"default"}) {
		t.Errorf("alice's initial selection = %v, want the configured [default]", scope.Actives)
	}
}

// --namespace-scope is the real extent of the cache, so it still clamps a
// plain read whatever the pick. The omnibar's globalNs=1 search keeps skipping
// that clamp so cluster-scoped hits (Namespaces, Nodes, ...) a user may list
// stay in its results.
func TestProxyAuth_PlainReadsKeepForcedCacheScope(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("default")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})
	// Sensitive cluster-scoped kinds are authorized by a live
	// SubjectAccessReview; this apiserver allows every one.
	apiserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "SubjectAccessReview", "apiVersion": "authorization.k8s.io/v1",
			"status": map[string]any{"allowed": true},
		})
	}))
	t.Cleanup(apiserver.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: apiserver.URL})
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	prevClient := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prevClient) })

	env := newAuthTestServer(t)
	env.srv.permCache.Set("root", nil, &pkgauth.UserPermissions{AllowedNamespaces: nil})

	hits := func(path string) []string {
		t.Helper()
		resp := env.authGet(t, path, "root", "")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, resp.StatusCode)
		}
		var body struct {
			Hits []struct {
				Name string `json:"name"`
			} `json:"hits"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		var names []string
		for _, h := range body.Hits {
			names = append(names, h.Name)
		}
		slices.Sort(names)
		return names
	}
	if got := hits("/api/search?q=kind:Deployment"); !slices.Equal(got, []string{"nginx"}) {
		t.Errorf("plain search under --namespace-scope = %v, want the pinned namespace's [nginx]", got)
	}
	if got := hits("/api/search?q=kind:Namespace&globalNs=1"); !slices.Equal(got, []string{"broken", "default"}) {
		t.Errorf("global search for Namespaces under --namespace-scope = %v, want [broken default]", got)
	}

	// A namespace outside the pin is never read, so the list says so rather
	// than answering an empty list.
	resp := env.authGet(t, "/api/resources/deployments?namespaces=broken", "root", "")
	defer func() { _ = resp.Body.Close() }()
	var failure struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StatusCode != http.StatusConflict || failure.ErrorCode != "outside_namespace_pin" {
		t.Errorf("list outside the pinned namespace = status %d, code %q, want 409 outside_namespace_pin", resp.StatusCode, failure.ErrorCode)
	}
}

// seedTimelineAcrossNamespaces puts one change in each fixture namespace into
// a fresh in-memory timeline store, restoring the suite's global store after.
func seedTimelineAcrossNamespaces(t *testing.T) {
	t.Helper()
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})
	base := time.Now().Add(-time.Minute)
	for i, ref := range [][2]string{{"default", "nginx"}, {"broken", "stuck-app"}} {
		if err := timeline.GetStore().Append(t.Context(), timeline.TimelineEvent{
			ID: "ev-" + ref[1], Timestamp: base.Add(time.Duration(i) * time.Second),
			Source: timeline.SourceInformer, Kind: "Deployment", APIVersion: "apps/v1",
			Namespace: ref[0], Name: ref[1], EventType: timeline.EventTypeUpdate,
			ClusterContext: k8s.ActiveClusterContext(),
		}); err != nil {
			t.Fatalf("Append %s: %v", ref[1], err)
		}
	}
}

// A caller who can't see a namespace gets the same answer for it whether or
// not it is outside the --namespace-scope pin, so guessing names doesn't
// reveal which namespace Radar is pinned to. "Outside the pin" is said only
// about a namespace the caller can see.
func TestProxyAuth_PinIsNotRevealedToGuesses(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("default")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})
	env := newAuthTestServer(t)
	for user, namespaces := range map[string][]string{"bob": {"broken"}, "carol": {"broken", "default"}} {
		perms := &pkgauth.UserPermissions{AllowedNamespaces: namespaces}
		for _, ns := range namespaces {
			perms.SetCanI("list", "apps", "deployments", ns, true)
		}
		env.srv.permCache.Set(user, nil, perms)
	}
	code := func(user, ns string) (int, string) {
		t.Helper()
		resp := env.authGet(t, "/api/resources/deployments?namespaces="+ns, user, "")
		defer func() { _ = resp.Body.Close() }()
		var failure struct {
			ErrorCode string `json:"error_code"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
			t.Fatalf("decode %s as %s: %v", ns, user, err)
		}
		return resp.StatusCode, failure.ErrorCode
	}
	// bob can see neither default (inside the pin) nor elsewhere (outside it).
	insideStatus, inside := code("bob", "default")
	outsideStatus, outside := code("bob", "elsewhere")
	if inside != "user_namespace_not_visible" || outside != inside || outsideStatus != insideStatus {
		t.Errorf("hidden namespaces answered differently: inside the pin %d %q, outside %d %q", insideStatus, inside, outsideStatus, outside)
	}
	// carol can see broken, which the pin leaves out.
	if status, got := code("carol", "broken"); status != http.StatusConflict || got != "outside_namespace_pin" {
		t.Errorf("visible namespace outside the pin = %d %q, want 409 outside_namespace_pin", status, got)
	}
}

// A caller who can see every namespace is told a named namespace doesn't
// exist rather than shown an empty list, and before the --namespace-scope pin:
// a mistyped name is the likelier cause. A real namespace beside it is still
// read, and one outside the pin is still outside_namespace_pin.
func TestProxyAuth_MissingNamespaceIsNotFound(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	env := newAuthTestServer(t)
	env.srv.permCache.Set("root", nil, &pkgauth.UserPermissions{AllowedNamespaces: nil})
	type body struct {
		Error     string `json:"error"`
		ErrorCode string `json:"error_code"`
		Coverage  struct {
			Gaps []struct {
				Code       string `json:"code"`
				Namespaces struct {
					Names []string `json:"names"`
				} `json:"namespaces"`
			} `json:"gaps"`
		} `json:"coverage"`
		Items []json.RawMessage `json:"items"`
	}
	fetch := func(path string) (int, body) {
		t.Helper()
		resp := env.authGet(t, path, "root", "")
		defer func() { _ = resp.Body.Close() }()
		var b body
		if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return resp.StatusCode, b
	}
	if status, b := fetch("/api/resources/deployments?namespaces=typo"); status != http.StatusNotFound || b.ErrorCode != "namespace_not_found" || b.Error != `namespace "typo" doesn't exist in this cluster` {
		t.Errorf("only a missing namespace = %d %q %q, want 404 namespace_not_found naming typo", status, b.ErrorCode, b.Error)
	}
	status, b := fetch("/api/resources/deployments?namespaces=default,typo")
	if status != http.StatusOK || len(b.Items) != 1 || len(b.Coverage.Gaps) != 1 || b.Coverage.Gaps[0].Code != "namespace_not_found" || !slices.Equal(b.Coverage.Gaps[0].Namespaces.Names, []string{"typo"}) {
		t.Errorf("missing beside existing = %d %+v, want default's row and a namespace_not_found gap for typo", status, b)
	}
	if status, b := fetch("/api/resources/deployments/typo/nginx"); status != http.StatusNotFound || b.ErrorCode != "namespace_not_found" {
		t.Errorf("get in a missing namespace = %d %q, want 404 namespace_not_found", status, b.ErrorCode)
	}

	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("default")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})
	if status, b := fetch("/api/resources/deployments?namespaces=typo"); status != http.StatusNotFound || b.ErrorCode != "namespace_not_found" {
		t.Errorf("missing namespace outside the pin = %d %q, want 404 namespace_not_found", status, b.ErrorCode)
	}
	if status, b := fetch("/api/resources/deployments?namespaces=broken"); status != http.StatusConflict || b.ErrorCode != "outside_namespace_pin" || b.Error != `namespace "broken" is outside Radar's --namespace-scope pin` {
		t.Errorf("existing namespace outside the pin = %d %q %q, want 409 outside_namespace_pin naming broken", status, b.ErrorCode, b.Error)
	}
	if status, b := fetch("/api/resources/deployments/typo/nginx"); status != http.StatusNotFound || b.ErrorCode != "namespace_not_found" {
		t.Errorf("get in a missing namespace outside the pin = %d %q, want 404 namespace_not_found", status, b.ErrorCode)
	}
}

// A missing namespace is one gap even when exact kind authorization would
// also deny it: it doesn't exist, so the denial says nothing.
func TestProxyAuth_MissingNamespaceIsOneGapForExactKinds(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	env := newAuthTestServer(t)
	perms := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "secrets", "", false)
	perms.SetCanI("list", "", "secrets", "default", true)
	perms.SetCanI("list", "", "secrets", "ghost", false)
	env.srv.permCache.Set("root", nil, perms)
	resp := env.authGet(t, "/api/resources/secrets?namespaces=default,ghost", "root", "")
	defer func() { _ = resp.Body.Close() }()
	var list testResourceListEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Coverage.Gaps) != 1 || list.Coverage.Gaps[0].Code != "namespace_not_found" {
		t.Fatalf("gaps %+v", list.Coverage.Gaps)
	}
}
