package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
)

// seedInClusterArgoApp registers an in-cluster Argo Application in the dynamic
// resource cache so handleArgoResourceDiff's destination gate (which fails closed
// if it can't read the app) can confirm the destination and proceed.
func seedInClusterArgoApp(t *testing.T, namespace, name string) {
	t.Helper()
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{appGVR: "ApplicationList"},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": name, "namespace": namespace},
			"spec":       map[string]any{"destination": map[string]any{"name": "in-cluster"}},
		}},
	)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application", Name: "applications", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatalf("seed argo app: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
}

// managedResourcesFunc serves the /managed-resources response for a fake Argo
// CD API server. It receives the incoming request so tests can vary behavior
// (return items, or fail with a status).
type managedResourcesFunc func(w http.ResponseWriter, r *http.Request)

// startFakeArgoServer stands up an Argo CD API server that accepts "good-token"
// for reachability + session probes and delegates the managed-resources call to
// managedFn. It returns the server and an atomic counter of managed-resources
// hits (so tests can assert an RBAC deny never reached the upstream).
func startFakeArgoServer(t *testing.T, managedFn managedResourcesFunc) (*httptest.Server, *int64) {
	t.Helper()
	var managedHits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Version":"v2.13.0"}`))
	})
	mux.HandleFunc("/api/v1/session/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid session"}`))
			return
		}
		_, _ = w.Write([]byte(`{"loggedIn":true,"username":"admin"}`))
	})
	mux.HandleFunc("/api/v1/applications/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/managed-resources") {
			atomic.AddInt64(&managedHits, 1)
			managedFn(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/metadata") {
			_, _ = w.Write([]byte(`{"author":"Jane Doe","date":"2026-07-09T12:00:00Z","message":"fix: bump memory","signatureInfo":"gpg: Good signature"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/v1/repositories", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[
			{"repo":"https://github.com/org/broken.git","connectionState":{"status":"Failed","message":"authentication required"}},
			{"repo":"https://github.com/org/healthy","connectionState":{"status":"Successful"}}
		]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &managedHits
}

// connectArgo points the global Argo manager at url and probes it so
// argocd.Get() returns connected. Cleanup resets the manager.
func connectArgo(t *testing.T, url string) {
	t.Helper()
	argocd.SetConfig(url, "good-token", false, true)
	t.Cleanup(func() { argocd.SetConfig("", "", false, true) })
	if err := argocd.Probe(context.Background()); err != nil {
		t.Fatalf("probe fake Argo: %v", err)
	}
}

// managedResourcesJSON marshals items into the {"items":[...]} envelope the
// managed-resources endpoint returns.
func managedResourcesJSON(t *testing.T, items ...argoapi.ResourceDiff) []byte {
	t.Helper()
	body, err := json.Marshal(struct {
		Items []argoapi.ResourceDiff `json:"items"`
	}{Items: items})
	if err != nil {
		t.Fatalf("marshal managed-resources: %v", err)
	}
	return body
}

// jsonManifest marshals a manifest map to the JSON string Argo stores in its
// *State fields.
func jsonManifest(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(b)
}

// newDiffRequest builds a resource-diff request with the Application ref bound
// as chi URL params, for direct handler invocation (no auth user → RBAC gates
// pass trivially).
func newDiffRequest(appNamespace, appName, query string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", appNamespace)
	rctx.URLParams.Add("name", appName)
	req := httptest.NewRequest(http.MethodGet, "/api/argo/applications/"+appNamespace+"/"+appName+"/resource-diff?"+query, nil)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func newRevMetaRequest(appNamespace, appName, query string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", appNamespace)
	rctx.URLParams.Add("name", appName)
	req := httptest.NewRequest(http.MethodGet, "/api/argo/applications/"+appNamespace+"/"+appName+"/revision-metadata?"+query, nil)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestArgoResourceDiff_HappyPath(t *testing.T) {
	seedInClusterArgoApp(t, "argocd", "guestbook")
	items := managedResourcesJSON(t, argoapi.ResourceDiff{
		Group:     "apps",
		Kind:      "Deployment",
		Namespace: "default",
		Name:      "guestbook-ui",
		PredictedLiveState: jsonManifest(t, map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "guestbook-ui", "namespace": "default"},
			"spec":     map[string]any{"replicas": 3},
		}),
		NormalizedLiveState: jsonManifest(t, map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "guestbook-ui", "namespace": "default"},
			"spec":     map[string]any{"replicas": 1},
		}),
	})
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(items)
	})
	connectArgo(t, srv.URL)

	req := newDiffRequest("argocd", "guestbook", "group=apps&kind=Deployment&resourceNamespace=default&resourceName=guestbook-ui")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoResourceDiff(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp argoResourceDiffResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Source != "argocd-api" {
		t.Errorf("source = %q, want argocd-api", resp.Source)
	}
	if resp.Redacted {
		t.Errorf("Deployment diff must not be marked redacted")
	}
	if !strings.Contains(resp.Desired, "replicas: 3") {
		t.Errorf("desired YAML missing replicas: 3:\n%s", resp.Desired)
	}
	if !strings.Contains(resp.Live, "replicas: 1") {
		t.Errorf("live YAML missing replicas: 1:\n%s", resp.Live)
	}
	if len(resp.FieldEntries) == 0 {
		t.Fatalf("expected field entries for spec.replicas change")
	}
	var foundReplicas bool
	for _, e := range resp.FieldEntries {
		if e.Path == "spec.replicas" {
			foundReplicas = true
		}
	}
	if !foundReplicas {
		t.Errorf("field entries missing spec.replicas: %+v", resp.FieldEntries)
	}
}

func TestArgoResourceDiff_SecretRedacted(t *testing.T) {
	seedInClusterArgoApp(t, "argocd", "guestbook")
	newPass := base64.StdEncoding.EncodeToString([]byte("BRAND-NEW-PASSWORD"))
	oldPass := base64.StdEncoding.EncodeToString([]byte("STALE-OLD-PASSWORD"))
	shared := base64.StdEncoding.EncodeToString([]byte("SHARED-UNCHANGED"))
	liveOnly := base64.StdEncoding.EncodeToString([]byte("LIVE-ONLY-VALUE"))

	desired := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": "app-secret", "namespace": "default",
			// last-applied embeds the raw data — must be stripped before serialize.
			"annotations": map[string]any{
				lastAppliedAnnotation: `{"data":{"password":"` + newPass + `"}}`,
			},
		},
		"data": map[string]any{"password": newPass, "shared": shared},
	}
	live := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "app-secret", "namespace": "default"},
		"data":     map[string]any{"password": oldPass, "shared": shared, "extra": liveOnly},
	}
	items := managedResourcesJSON(t, argoapi.ResourceDiff{
		Group: "", Kind: "Secret", Namespace: "default", Name: "app-secret",
		PredictedLiveState:  jsonManifest(t, desired),
		NormalizedLiveState: jsonManifest(t, live),
	})
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(items)
	})
	connectArgo(t, srv.URL)

	req := newDiffRequest("argocd", "guestbook", "kind=Secret&resourceNamespace=default&resourceName=app-secret")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoResourceDiff(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// No raw secret material — in any field, including the last-applied blob.
	for _, secret := range []string{newPass, oldPass, shared, liveOnly} {
		if strings.Contains(body, secret) {
			t.Errorf("response leaked a raw secret value %q:\n%s", secret, body)
		}
	}
	if strings.Contains(body, lastAppliedAnnotation) {
		t.Errorf("last-applied annotation must be stripped from a Secret diff:\n%s", body)
	}

	var resp argoResourceDiffResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Redacted {
		t.Errorf("Secret diff must set redacted=true")
	}
	// password differs → changed on both sides; shared identical → unchanged;
	// extra present only on live → changed.
	if !strings.Contains(resp.Desired, "password: "+redactedChanged) {
		t.Errorf("desired.password should be marked changed:\n%s", resp.Desired)
	}
	if !strings.Contains(resp.Live, "password: "+redactedChanged) {
		t.Errorf("live.password should be marked changed:\n%s", resp.Live)
	}
	if !strings.Contains(resp.Desired, "shared: "+redactedUnchanged) {
		t.Errorf("desired.shared should be marked unchanged:\n%s", resp.Desired)
	}
	if !strings.Contains(resp.Live, "extra: "+redactedChanged) {
		t.Errorf("live-only key should be marked changed:\n%s", resp.Live)
	}
}

// TestArgoResourceDiff_SecretRedactionFailClosed pins the fail-closed paths:
// a secret smuggled into an annotation value, and a malformed scalar
// stringData field, must both be masked — not just the well-formed data map.
func TestArgoResourceDiff_SecretRedactionFailClosed(t *testing.T) {
	seedInClusterArgoApp(t, "argocd", "guestbook")
	annoSecret := "super-secret-bootstrap-token"
	scalarSecret := "raw-scalar-secret"

	desired := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": "app-secret", "namespace": "default",
			"annotations": map[string]any{"bootstrap.kubernetes.io/token": annoSecret},
		},
		// Malformed: stringData as a scalar rather than a map.
		"stringData": scalarSecret,
	}
	live := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "app-secret", "namespace": "default"},
	}
	items := managedResourcesJSON(t, argoapi.ResourceDiff{
		Group: "", Kind: "Secret", Namespace: "default", Name: "app-secret",
		PredictedLiveState:  jsonManifest(t, desired),
		NormalizedLiveState: jsonManifest(t, live),
	})
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(items)
	})
	connectArgo(t, srv.URL)

	req := newDiffRequest("argocd", "guestbook", "kind=Secret&resourceNamespace=default&resourceName=app-secret")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoResourceDiff(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{annoSecret, scalarSecret} {
		if strings.Contains(body, secret) {
			t.Errorf("response leaked secret material %q:\n%s", secret, body)
		}
	}
}

func TestArgoResourceDiff_NotInManagedSet(t *testing.T) {
	seedInClusterArgoApp(t, "argocd", "guestbook")
	items := managedResourcesJSON(t, argoapi.ResourceDiff{
		Group: "apps", Kind: "Deployment", Namespace: "default", Name: "other-workload",
		PredictedLiveState: jsonManifest(t, map[string]any{"kind": "Deployment"}),
	})
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(items)
	})
	connectArgo(t, srv.URL)

	req := newDiffRequest("argocd", "guestbook", "group=apps&kind=Deployment&resourceNamespace=default&resourceName=missing-ui")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoResourceDiff(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestArgoResourceDiff_NotConnected(t *testing.T) {
	seedInClusterArgoApp(t, "argocd", "guestbook")
	// Ensure the manager is unconfigured/disconnected: Get() returns false.
	argocd.SetConfig("", "", false, true)
	t.Cleanup(func() { argocd.SetConfig("", "", false, true) })

	req := newDiffRequest("argocd", "guestbook", "kind=Deployment&resourceNamespace=default&resourceName=guestbook-ui")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoResourceDiff(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not connected") {
		t.Errorf("body = %s, want not-connected message", w.Body.String())
	}
}

// TestArgoResourceDiff_EmptyNamespace pins that a request with no Application
// namespace is rejected with 400 before any RBAC gate or Argo call — an empty
// segment would otherwise skip the namespace-access check.
func TestArgoResourceDiff_EmptyNamespace(t *testing.T) {
	req := newDiffRequest("", "guestbook", "kind=Deployment&resourceNamespace=default&resourceName=guestbook-ui")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoResourceDiff(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "namespace is required") {
		t.Errorf("body = %s, want namespace-required message", w.Body.String())
	}
}

func TestArgoResourceDiff_TokenRejectedUpstream(t *testing.T) {
	seedInClusterArgoApp(t, "argocd", "guestbook")
	// Probe succeeds (session valid), but the managed-resources call is rejected
	// with 401 — exercises the ErrUnauthorized → 403 mapping.
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"token expired"}`))
	})
	connectArgo(t, srv.URL)

	req := newDiffRequest("argocd", "guestbook", "group=apps&kind=Deployment&resourceNamespace=default&resourceName=guestbook-ui")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoResourceDiff(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "token") {
		t.Errorf("body = %s, want token-specific message", w.Body.String())
	}
}

// TestArgoResourceDiff_RBACDeniesBeforeArgoCall pins the dual gate: a user who
// can see the Application's namespace but lacks `get secrets` in the target
// namespace is denied by the per-resource preflight BEFORE any Argo API call.
func TestArgoResourceDiff_RBACDeniesBeforeArgoCall(t *testing.T) {
	env := newAuthTestServer(t)
	// alice can see both the app namespace (argocd) and the target namespace
	// (default), but per-namespace `get secrets` in default is denied.
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"argocd", "default"},
	})
	seedServerSecretGetCanI(t, env, "alice", nil, []string{"default"})

	srv, managedHits := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(managedResourcesJSON(t, argoapi.ResourceDiff{
			Group: "", Kind: "Secret", Namespace: "default", Name: "app-secret",
			NormalizedLiveState: jsonManifest(t, map[string]any{"kind": "Secret"}),
		}))
	})
	connectArgo(t, srv.URL)

	resp := env.authGet(t, "/api/argo/applications/argocd/guestbook/resource-diff?kind=Secret&resourceNamespace=default&resourceName=app-secret", "alice", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if got := atomic.LoadInt64(managedHits); got != 0 {
		t.Errorf("Argo managed-resources was called %d times; RBAC deny must short-circuit before any Argo call", got)
	}
}

func TestArgoRevisionMetadata_HappyPath(t *testing.T) {
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(managedResourcesJSON(t))
	})
	connectArgo(t, srv.URL)

	req := newRevMetaRequest("argocd", "guestbook", "revision=abc123")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoRevisionMetadata(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var meta argoapi.RevisionMetadata
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if meta.Author != "Jane Doe" || meta.Message != "fix: bump memory" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestArgoRevisionMetadata_RequiresRevision(t *testing.T) {
	req := newRevMetaRequest("argocd", "guestbook", "")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoRevisionMetadata(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestArgoRevisionMetadata_EmptyNamespace(t *testing.T) {
	req := newRevMetaRequest("", "guestbook", "revision=abc")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoRevisionMetadata(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestArgoRevisionMetadata_NotConnected(t *testing.T) {
	argocd.SetConfig("", "", false, true)
	t.Cleanup(func() { argocd.SetConfig("", "", false, true) })

	req := newRevMetaRequest("argocd", "guestbook", "revision=abc")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoRevisionMetadata(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
}

func argoAppRoot(sources ...string) *unstructured.Unstructured {
	spec := map[string]any{}
	switch {
	case len(sources) == 1:
		spec["source"] = map[string]any{"repoURL": sources[0]}
	case len(sources) > 1:
		arr := make([]any, 0, len(sources))
		for _, s := range sources {
			arr = append(arr, map[string]any{"repoURL": s})
		}
		spec["sources"] = arr
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "guestbook", "namespace": "argocd"},
		"spec":     spec,
	}}
}

// waitRepoCache blocks until the manager's async repository refresh has
// populated the cache (RepositoriesCached is non-blocking, so the first call
// only kicks the background fetch).
func waitRepoCache(t *testing.T) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if len(argocd.RepositoriesCached()) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("repository cache did not populate")
}

func TestEnrichArgoRepoHealth_FailedRepo(t *testing.T) {
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(managedResourcesJSON(t)) })
	connectArgo(t, srv.URL)
	waitRepoCache(t)

	insight := &gitopsinsights.Insight{}
	// Root sources ".../broken" (no .git); the repo list stores ".../broken.git"
	// — the match must survive the .git normalization.
	(&Server{}).enrichArgoRepoHealth(argoAppRoot("https://github.com/org/broken"), insight)

	if len(insight.Issues) != 1 {
		t.Fatalf("issues = %d, want 1: %+v", len(insight.Issues), insight.Issues)
	}
	iss := insight.Issues[0]
	if iss.Reason != "RepoUnreachable" || iss.Severity != gitopsinsights.SeverityWarning {
		t.Errorf("issue = %+v", iss)
	}
	if !strings.Contains(iss.Message, "github.com/org/broken") {
		t.Errorf("message = %q", iss.Message)
	}
	if iss.RawMessage != "authentication required" {
		t.Errorf("rawMessage = %q, want the Argo connection error", iss.RawMessage)
	}
}

func TestEnrichArgoRepoHealth_MergesIntoComparisonError(t *testing.T) {
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(managedResourcesJSON(t)) })
	connectArgo(t, srv.URL)
	waitRepoCache(t)

	// The app already carries the ComparisonError symptom (Argo couldn't load
	// the desired state). The failed repo connection is its cause — it must
	// fold into that one critical issue, not stack a second warning beside it.
	insight := &gitopsinsights.Insight{Issues: []gitopsinsights.Issue{{
		Severity: gitopsinsights.SeverityCritical,
		Scope:    gitopsinsights.ScopeCondition,
		Reason:   "ComparisonError",
		Message:  "Failed to load target state: failed to generate manifest for source 1 of 1",
	}}}
	(&Server{}).enrichArgoRepoHealth(argoAppRoot("https://github.com/org/broken"), insight)

	if len(insight.Issues) != 1 {
		t.Fatalf("issues = %d, want 1 (merged, not stacked): %+v", len(insight.Issues), insight.Issues)
	}
	iss := insight.Issues[0]
	if iss.Reason != "ComparisonError" || iss.Severity != gitopsinsights.SeverityCritical {
		t.Errorf("merged issue must stay the critical ComparisonError: %+v", iss)
	}
	if !strings.Contains(iss.Message, "github.com/org/broken") {
		t.Errorf("merged message must name the repo: %q", iss.Message)
	}
	if iss.Action == "" {
		t.Errorf("merged issue must carry the repo fix action")
	}
}

func TestEnrichArgoRepoHealth_HealthyRepoNoIssue(t *testing.T) {
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(managedResourcesJSON(t)) })
	connectArgo(t, srv.URL)
	waitRepoCache(t)

	insight := &gitopsinsights.Insight{}
	(&Server{}).enrichArgoRepoHealth(argoAppRoot("https://github.com/org/healthy"), insight)
	if len(insight.Issues) != 0 {
		t.Fatalf("healthy repo must not add an issue: %+v", insight.Issues)
	}
}

func TestEnrichArgoRepoHealth_UnknownRepoNoIssue(t *testing.T) {
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(managedResourcesJSON(t)) })
	connectArgo(t, srv.URL)
	waitRepoCache(t)

	insight := &gitopsinsights.Insight{}
	(&Server{}).enrichArgoRepoHealth(argoAppRoot("https://github.com/org/not-tracked"), insight)
	if len(insight.Issues) != 0 {
		t.Fatalf("unknown repo must be treated as unknown, not unhealthy: %+v", insight.Issues)
	}
}

func TestEnrichArgoRepoHealth_MultiSource(t *testing.T) {
	srv, _ := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(managedResourcesJSON(t)) })
	connectArgo(t, srv.URL)
	waitRepoCache(t)

	insight := &gitopsinsights.Insight{}
	(&Server{}).enrichArgoRepoHealth(argoAppRoot("https://github.com/org/healthy", "https://github.com/org/broken"), insight)
	if len(insight.Issues) != 1 {
		t.Fatalf("issues = %d, want 1 (only the broken source): %+v", len(insight.Issues), insight.Issues)
	}
}

func TestEnrichArgoRepoHealth_NotConnectedNoIssue(t *testing.T) {
	argocd.SetConfig("", "", false, true)
	t.Cleanup(func() { argocd.SetConfig("", "", false, true) })

	insight := &gitopsinsights.Insight{}
	(&Server{}).enrichArgoRepoHealth(argoAppRoot("https://github.com/org/broken"), insight)
	if len(insight.Issues) != 0 {
		t.Fatalf("disconnected Argo must not add an issue (best-effort): %+v", insight.Issues)
	}
}

func TestArgoRevisionMetadata_UnderscoreNamespaceRejected(t *testing.T) {
	// The web client sends "_" for an empty namespace segment; it must normalize
	// to "" and be rejected (an Argo Application always has a namespace), not
	// treated as a literal "_" namespace for RBAC / the upstream lookup.
	req := newRevMetaRequest("_", "guestbook", "revision=abc")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoRevisionMetadata(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestIsInClusterDestination(t *testing.T) {
	mk := func(dest map[string]any) *unstructured.Unstructured {
		spec := map[string]any{}
		if dest != nil {
			spec["destination"] = dest
		}
		return &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	}
	cases := []struct {
		name string
		app  *unstructured.Unstructured
		want bool
	}{
		{"nil app fails closed", nil, false},
		{"no destination defaults local", mk(nil), true},
		{"empty destination defaults local", mk(map[string]any{}), true},
		{"name in-cluster", mk(map[string]any{"name": "in-cluster"}), true},
		{"local api server url", mk(map[string]any{"server": "https://kubernetes.default.svc"}), true},
		{"local api server url with port", mk(map[string]any{"server": "https://kubernetes.default.svc:443"}), true},
		{"local api server FQDN", mk(map[string]any{"server": "https://kubernetes.default.svc.cluster.local"}), true},
		{"local api server FQDN with port", mk(map[string]any{"server": "https://kubernetes.default.svc.cluster.local:443"}), true},
		{"remote named cluster", mk(map[string]any{"name": "prod-spoke"}), false},
		{"remote server url", mk(map[string]any{"server": "https://10.0.0.1:6443"}), false},
		{"remote eks url", mk(map[string]any{"server": "https://abc123.gr7.us-east-1.eks.amazonaws.com"}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInClusterDestination(tc.app); got != tc.want {
				t.Errorf("isInClusterDestination = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEnrichComparisonErrorWithRepos_MultiRepoNoOverwrite(t *testing.T) {
	// Two failed source repos folding into one ComparisonError: both URLs must
	// survive (the earlier single-repo fold overwrote the first with the last),
	// the issue stays critical, and no standalone warnings are added.
	insight := &gitopsinsights.Insight{Issues: []gitopsinsights.Issue{{
		Severity: gitopsinsights.SeverityCritical,
		Scope:    gitopsinsights.ScopeCondition,
		Reason:   "ComparisonError",
		Message:  "Failed to load target state",
	}}}
	failed := []failedRepo{
		{url: "https://github.com/org/broken", connErr: "authentication required"},
		{url: "https://github.com/org/broken2", connErr: "repository not found"},
	}
	if !enrichComparisonErrorWithRepos(insight, failed) {
		t.Fatal("expected the failed repos to fold into the ComparisonError")
	}
	if len(insight.Issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(insight.Issues))
	}
	iss := insight.Issues[0]
	if iss.Severity != gitopsinsights.SeverityCritical {
		t.Errorf("severity = %v, want critical (unchanged)", iss.Severity)
	}
	if !strings.Contains(iss.Message, "github.com/org/broken") || !strings.Contains(iss.Message, "github.com/org/broken2") {
		t.Errorf("merged message must name BOTH failed repos: %q", iss.Message)
	}
}

func TestArgoResourceDiff_RemoteDestinationRefused(t *testing.T) {
	// A hub-spoke Application whose destination is a remote cluster: the local
	// SARs can't authorize that read, so the diff must be refused before any
	// manifest is served — even though the managed-resources call would succeed.
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{appGVR: "ApplicationList"},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": "guestbook", "namespace": "argocd"},
			"spec":     map[string]any{"destination": map[string]any{"server": "https://spoke.example.com:6443"}},
		}},
	)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application", Name: "applications", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatalf("seed argo app: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	srv, hits := startFakeArgoServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(managedResourcesJSON(t)) })
	connectArgo(t, srv.URL)

	req := newDiffRequest("argocd", "guestbook", "group=apps&kind=Deployment&resourceNamespace=default&resourceName=guestbook-ui")
	w := httptest.NewRecorder()
	(&Server{}).handleArgoResourceDiff(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a remote-destination app; body = %s", w.Code, w.Body.String())
	}
	if atomic.LoadInt64(hits) != 0 {
		t.Errorf("remote-destination diff must be refused before any managed-resources call; hits = %d", atomic.LoadInt64(hits))
	}
}
