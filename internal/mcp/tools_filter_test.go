package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// These tests verify that each MCP read handler integrates with the
// per-user filter helpers — i.e. results are narrowed to the calling
// user's allowed namespaces, cluster-only kinds are hidden from
// non-cluster-admins, and no-auth callers see everything in the cache.
//
// They use a real (fake-backed) ResourceCache rather than mocks so the
// data path matches production: cache holds objects across namespaces;
// filtering happens at handler-call time, not at cache-population time.

func setupFakeCacheForFilterTests(t *testing.T) {
	t.Helper()

	fakeClient := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "beta"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gamma"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},

		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "alpha-pod", Namespace: "alpha"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "beta-pod", Namespace: "beta"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gamma-pod", Namespace: "gamma"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},

		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}},

		// Seeded for per-namespace Secret RBAC tests. The cache holds
		// secrets across all three namespaces (mirrors the chart's optional
		// cluster-wide secrets grant for the SA); per-user RBAC must narrow
		// the view at read time.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-secret", Namespace: "alpha"}, Type: corev1.SecretTypeOpaque},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "beta-secret", Namespace: "beta"}, Type: corev1.SecretTypeOpaque},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gamma-secret", Namespace: "gamma"}, Type: corev1.SecretTypeOpaque},
	)

	if err := k8s.InitTestResourceCache(fakeClient); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(func() {
		k8s.ResetTestState()
		getPermCache().Invalidate()
	})

	// Mark connected so handlers don't bail early on requireConnected-style
	// checks. (The MCP handlers don't use requireConnected directly, but
	// downstream code paths do.)
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
}

// withRestrictedUser primes the perm cache for a namespace-restricted user
// (allowed = exactly the namespaces passed) and returns a context with that
// user attached. Use nil/empty allowed for "denied to all" testing.
//
// The user starts with NO cluster-scoped read permissions cached. Call
// grantClusterRead in the test to seed specific allowed (group, resource)
// tuples — anything not seeded will trigger a live SAR which fails closed
// (no K8s client in the test harness).
func withRestrictedUser(t *testing.T, username string, allowed []string) context.Context {
	t.Helper()
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: username, Groups: nil})
	getPermCache().Set(username, &pkgauth.UserPermissions{AllowedNamespaces: allowed})
	return ctx
}

// withClusterAdmin attaches a user whose perms have nil AllowedNamespaces
// (cluster-wide namespaced access). NOTE: this no longer implies cluster-
// scoped read access — those gates are per-(group, resource) SARs. Call
// grantClusterRead to seed specific cluster-scoped reads as authorized.
func withClusterAdmin(t *testing.T, username string) context.Context {
	t.Helper()
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: username, Groups: nil})
	getPermCache().Set(username, &pkgauth.UserPermissions{AllowedNamespaces: nil})
	return ctx
}

// grantClusterRead seeds the per-kind SAR cache so canReadClusterScopedKind
// returns the desired result without making a live call. Each gvr is
// "<group>/<resource>" (group="" for core), e.g. "/nodes", "rbac.authorization.k8s.io/clusterroles".
// Sets both list and get verbs.
func grantClusterRead(t *testing.T, username string, gvrs ...string) {
	t.Helper()
	perms := getPermCache().Get(username)
	if perms == nil {
		t.Fatalf("user %q not in perm cache; call withRestrictedUser/withClusterAdmin first", username)
	}
	for _, gvr := range gvrs {
		group, resource, ok := strings.Cut(gvr, "/")
		if !ok {
			t.Fatalf("grantClusterRead: bad GVR %q (need group/resource)", gvr)
		}
		perms.SetCanI("list", group, resource, "", true)
		perms.SetCanI("get", group, resource, "", true)
	}
}

// denyClusterRead is the explicit-deny counterpart. Use to verify cluster-
// scoped reads are gated even when the cache contains the resource.
func denyClusterRead(t *testing.T, username string, gvrs ...string) {
	t.Helper()
	perms := getPermCache().Get(username)
	if perms == nil {
		t.Fatalf("user %q not in perm cache", username)
	}
	for _, gvr := range gvrs {
		group, resource, ok := strings.Cut(gvr, "/")
		if !ok {
			t.Fatalf("denyClusterRead: bad GVR %q", gvr)
		}
		perms.SetCanI("list", group, resource, "", false)
		perms.SetCanI("get", group, resource, "", false)
	}
}

// extractText pulls the JSON-encoded payload out of an MCP CallToolResult.
// Our handlers always return a single TextContent block (toJSONResult marshals
// the data and wraps it in TextContent.Text).
func extractText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatalf("empty CallToolResult")
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	return tc.Text
}

// containsName checks the JSON payload for an object with the given name.
// Strict-string matching is fine because pod/namespace names in our fixture
// are unique.
func containsName(payload, name string) bool {
	return strings.Contains(payload, `"name":"`+name+`"`)
}

// TestHandleListResources_GroupRoutesToDynamic pins the group-aware
// short-circuit on the MCP list_resources path. For kind=services with
// no group, the typed core Service list returns the seeded fixture. For
// kind=services&group=serving.knative.dev, the handler must skip the
// typed cache (which is group-blind — it would silently return core
// Services and drop the group filter on the floor) and route through
// listDynamicResources instead. Mirrors the REST-side fix in
// handleAIListResources and the GET-side fix from PR #721.
//
// setupFakeCacheForFilterTests doesn't initialize the dynamic cache, so
// the dynamic call surfaces an error. listDynamicResources wraps it in
// "failed to list %s: …" — pin both that the result does NOT contain
// the core Service AND that the call returned the dynamic-cache error
// (proving the routing change is in place).
func TestHandleListResources_GroupRoutesToDynamic(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})

	// With no group: typed cache, but no Services in the fixture so
	// it's an empty list. Sanity check the baseline.
	_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "services", Namespace: "alpha"})
	if err != nil {
		t.Fatalf("baseline (no group): %v", err)
	}

	// With group=serving.knative.dev: must route to dynamic. The fake
	// cache has no dynamic discovery wired, so we expect an error
	// rather than a (wrong) 200 with typed core Services.
	_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "services", Namespace: "alpha", Group: "serving.knative.dev"})
	if err == nil {
		t.Fatalf("group=serving.knative.dev: expected dynamic-cache routing error (no discovery in test harness), got nil err — handler may have silently returned typed core Services (pre-fix bug)")
	}
	// The wrapped error should reflect the dynamic path, not a typed
	// cache lookup. Match loosely on shape so future error-text
	// refactors don't flake the test.
	if !strings.Contains(err.Error(), "services") {
		t.Errorf("error should mention services kind: %v", err)
	}
}

func TestHandleListResources_RestrictedUser(t *testing.T) {
	setupFakeCacheForFilterTests(t)

	// Alice can see "alpha" only.
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})

	// list pods (no namespace param) — should return alpha-pod only.
	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "pods"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	if !containsName(body, "alpha-pod") {
		t.Errorf("expected alpha-pod in result; got: %s", body)
	}
	if containsName(body, "beta-pod") || containsName(body, "gamma-pod") {
		t.Errorf("restricted user leaked other-namespace pods: %s", body)
	}
}

func TestHandleListResources_CommonAliases(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})

	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "po"})
	if err != nil {
		t.Fatalf("handleListResources po: %v", err)
	}
	body := extractText(t, result)
	if !containsName(body, "alpha-pod") {
		t.Errorf("expected alpha-pod via po alias; got: %s", body)
	}
	if containsName(body, "beta-pod") || containsName(body, "gamma-pod") {
		t.Errorf("po alias leaked other-namespace pods: %s", body)
	}

	result, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "no"})
	if err != nil {
		t.Fatalf("handleListResources no: %v", err)
	}
	if body := extractText(t, result); body != "[]" {
		t.Errorf("node alias must still respect cluster-scoped RBAC, got: %s", body)
	}
}

func TestHandleListResources_DeniedNamespace(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})

	// Alice asks for beta — empty result, no error.
	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "pods", Namespace: "beta"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	if containsName(body, "beta-pod") {
		t.Errorf("denied namespace leaked: %s", body)
	}
}

func TestHandleListResources_ClusterOnlyKindBlockedForRestricted(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	// Alice is namespace-restricted to alpha and lacks Node read RBAC.
	// Seed an explicit deny so the test doesn't make a live SAR.
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})
	denyClusterRead(t, "alice", "/nodes")

	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "nodes"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	if containsName(body, "node-1") || containsName(body, "node-2") {
		t.Errorf("restricted user saw cluster-only Node resources: %s", body)
	}
}

func TestHandleListResources_ClusterWidePodsButNoNodes(t *testing.T) {
	// Cluster-wide namespaced read access (AllowedNamespaces==nil) does NOT
	// imply cluster-scoped read access. Nodes still require a successful
	// per-kind SAR; deny it here and verify nothing leaks from the cache.
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "broad-pod-reader") // nil AllowedNamespaces
	denyClusterRead(t, "broad-pod-reader", "/nodes")

	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "nodes"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	if containsName(body, "node-1") || containsName(body, "node-2") {
		t.Errorf("user with cluster-wide pods but no nodes saw nodes: %s", body)
	}
}

func TestHandleGetDashboard_ClusterWidePodsButNoClusterScopedReads(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "dashboard-viewer")
	denyClusterRead(t, "dashboard-viewer", "/nodes", "/namespaces")

	result, _, err := handleGetDashboard(ctx, nil, dashboardInput{})
	if err != nil {
		t.Fatalf("handleGetDashboard: %v", err)
	}

	var body mcpDashboard
	if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
		t.Fatalf("unmarshal dashboard: %v", err)
	}
	if got := body.ResourceCounts["pods"]; got != 3 {
		t.Fatalf("expected cluster-wide pod count to remain visible, got %d", got)
	}
	if got := body.ResourceCounts["nodes"]; got != 0 {
		t.Fatalf("node count leaked without node read RBAC: %d", got)
	}
	if got := body.ResourceCounts["namespaces"]; got != 0 {
		t.Fatalf("namespace count leaked without namespace read RBAC: %d", got)
	}
	if body.Nodes.Total != 0 || len(body.VersionSkew) != 0 || body.TopologyNodes != 0 {
		t.Fatalf("cluster-scoped dashboard fields leaked: nodes=%+v versionSkew=%v topologyNodes=%d", body.Nodes, body.VersionSkew, body.TopologyNodes)
	}
}

func TestHandleListResources_ClusterAdminSeesEverything(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "admin")
	grantClusterRead(t, "admin", "/nodes") // explicitly granted via seeded SAR

	// Pods: cluster-wide namespaced reads.
	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "pods"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	for _, want := range []string{"alpha-pod", "beta-pod", "gamma-pod"} {
		if !containsName(body, want) {
			t.Errorf("cluster-admin missing %s: %s", want, body)
		}
	}

	// Nodes: granted via explicit SAR seed.
	result, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "nodes"})
	if err != nil {
		t.Fatalf("handleListResources nodes: %v", err)
	}
	body = extractText(t, result)
	if !containsName(body, "node-1") {
		t.Errorf("cluster-admin missing node-1: %s", body)
	}
}

func TestHandleListResources_NoAuthPassthrough(t *testing.T) {
	setupFakeCacheForFilterTests(t)

	// No user on context — every call passes through (local-binary case).
	result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "pods"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	for _, want := range []string{"alpha-pod", "beta-pod", "gamma-pod"} {
		if !containsName(body, want) {
			t.Errorf("no-auth passthrough missing %s: %s", want, body)
		}
	}
}

func TestHandleGetResource_DeniedNamespace(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})

	_, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "pods", Namespace: "beta", Name: "beta-pod"})
	if err == nil {
		t.Fatal("expected forbidden error for denied namespace, got nil")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected 'forbidden' in error, got: %v", err)
	}
}

func TestHandleGetResource_ClusterOnlyForbiddenForRestricted(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})
	denyClusterRead(t, "alice", "/nodes")

	// Cluster-scoped get (empty namespace) requires explicit cluster-scoped
	// read RBAC; this user has neither namespace access nor seeded SAR allow.
	_, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "nodes", Namespace: "", Name: "node-1"})
	if err == nil {
		t.Fatal("expected forbidden error for cluster-scoped read, got nil")
	}
}

func TestHandleListResources_NamespacesRequiresListNamespacesSAR(t *testing.T) {
	// list_resources(kind=namespaces) returns full Namespace objects.
	// AllowedNamespaces==nil (cluster-wide-pods sentinel) does NOT license
	// Namespace metadata reads. Pin the strict gate; the synthesized
	// list_namespaces tool covers the picker UX for restricted users.
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "alice")
	denyClusterRead(t, "alice", "/namespaces")

	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "namespaces"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	if containsName(body, "alpha") || containsName(body, "beta") || containsName(body, "gamma") {
		t.Errorf("namespaces leaked without list-namespaces SAR: %s", body)
	}
}

func TestHandleGetResource_NamespacesRequiresGetNamespacesSAR(t *testing.T) {
	// get_resource(kind=namespaces, name=alpha) returns the full Namespace
	// object. Read access to resources IN alpha is not the same as
	// get-namespace-alpha (which needs ClusterRole on namespaces).
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})
	denyClusterRead(t, "alice", "/namespaces")

	_, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "namespaces", Name: "alpha"})
	if err == nil {
		t.Fatal("expected forbidden error without get-namespaces SAR, got nil")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected 'forbidden' in error, got: %v", err)
	}
}

func TestHandleListNamespaces_FiltersToAllowed(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha", "beta"})

	result, _, err := handleListNamespaces(ctx, nil, struct{}{})
	if err != nil {
		t.Fatalf("handleListNamespaces: %v", err)
	}
	body := extractText(t, result)
	if !containsName(body, "alpha") || !containsName(body, "beta") {
		t.Errorf("expected alpha and beta in namespace list: %s", body)
	}
	if containsName(body, "gamma") {
		t.Errorf("denied namespace gamma leaked: %s", body)
	}
}

func TestHandleListNamespaces_NoAuth(t *testing.T) {
	setupFakeCacheForFilterTests(t)

	result, _, err := handleListNamespaces(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("handleListNamespaces: %v", err)
	}
	body := extractText(t, result)
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !containsName(body, want) {
			t.Errorf("no-auth missing %s: %s", want, body)
		}
	}
}

func TestHandleGetEvents_RestrictedAggregatesAllowed(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})

	// No events in the fake cache, but the call should not error and should
	// not attempt to read beta/gamma. We're verifying the empty-result path
	// short-circuits cleanly.
	result, _, err := handleGetEvents(ctx, nil, eventsInput{Namespace: "beta"})
	if err != nil {
		t.Fatalf("handleGetEvents: %v", err)
	}
	body := extractText(t, result)
	if !strings.Contains(body, "[]") {
		t.Errorf("expected empty result for denied namespace, got: %s", body)
	}
}

// --- Per-namespace Secret RBAC ---
//
// The chart grants the Radar SA cluster-wide secrets so Helm release
// visibility works under auth modes. The cache therefore holds secrets
// across all namespaces; the server must gate reads per-user, per-namespace.
// These tests pin the gate at the MCP layer for list_resources / get_resource.

// seedSecretListCanI seeds the per-namespace canI cache for `list` on
// secrets only. Each list-handler test should call this (not both verbs)
// so a regression where the handler accidentally checks the wrong verb
// surfaces as a failed assertion.
func seedSecretListCanI(t *testing.T, username string, allowedNamespaces []string, deniedNamespaces []string) {
	t.Helper()
	seedSecretCanIVerb(t, username, "list", allowedNamespaces, deniedNamespaces)
}

// seedSecretGetCanI is the `get`-verb counterpart. See seedSecretListCanI.
func seedSecretGetCanI(t *testing.T, username string, allowedNamespaces []string, deniedNamespaces []string) {
	t.Helper()
	seedSecretCanIVerb(t, username, "get", allowedNamespaces, deniedNamespaces)
}

func seedSecretCanIVerb(t *testing.T, username, verb string, allowedNamespaces []string, deniedNamespaces []string) {
	t.Helper()
	perms := getPermCache().Get(username)
	if perms == nil {
		t.Fatalf("user %q not in perm cache", username)
	}
	for _, ns := range allowedNamespaces {
		perms.SetCanI(verb, "", "secrets", ns, true)
	}
	for _, ns := range deniedNamespaces {
		perms.SetCanI(verb, "", "secrets", ns, false)
	}
}

func TestHandleListResources_Secrets_DeniedInAllNamespaces(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	// alice has namespace access to alpha but no per-namespace secret read.
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})
	seedSecretListCanI(t, "alice", nil, []string{"alpha"})

	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "secrets"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	if containsName(body, "alpha-secret") {
		t.Errorf("denied secret leaked through namespace-only gate: %s", body)
	}
}

func TestHandleListResources_Secrets_PartialNamespaceAccess(t *testing.T) {
	// alice has namespace access to alpha AND beta, but per-namespace secret
	// read only in alpha. Should see alpha-secret only, not beta-secret —
	// even though beta is in her namespace allow-list.
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha", "beta"})
	seedSecretListCanI(t, "alice", []string{"alpha"}, []string{"beta"})

	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "secrets"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	if !containsName(body, "alpha-secret") {
		t.Errorf("expected alpha-secret in result: %s", body)
	}
	if containsName(body, "beta-secret") {
		t.Errorf("beta-secret leaked despite per-namespace secret deny: %s", body)
	}
}

func TestHandleListResources_Secrets_ClusterWideShape_NoSecretRBAC(t *testing.T) {
	// User with AllowedNamespaces==nil (cluster-wide namespace sentinel from
	// DiscoverNamespaces stage 1: list-pods cluster-wide) but no cluster-scope
	// secrets RBAC. Cluster-wide pod visibility does NOT imply cluster-wide
	// secret visibility — pin the cluster-scope SAR gate so this conflation
	// can't return.
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "broad-reader")
	perms := getPermCache().Get("broad-reader")
	if perms == nil {
		t.Fatalf("broad-reader not in cache")
	}
	perms.SetCanI("list", "", "secrets", "", false)

	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "secrets"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	for _, want := range []string{"alpha-secret", "beta-secret", "gamma-secret"} {
		if containsName(body, want) {
			t.Errorf("secret %q leaked to cluster-wide-pods user without secrets SAR: %s", want, body)
		}
	}
}

func TestHandleListResources_Secrets_ClusterWideShape_WithSecretRBAC(t *testing.T) {
	// Same shape as the previous test but with the cluster-scope secrets SAR
	// allowed — user sees every secret in the cache.
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "broad-reader")
	perms := getPermCache().Get("broad-reader")
	if perms == nil {
		t.Fatalf("broad-reader not in cache")
	}
	perms.SetCanI("list", "", "secrets", "", true)

	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "secrets"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	for _, want := range []string{"alpha-secret", "beta-secret", "gamma-secret"} {
		if !containsName(body, want) {
			t.Errorf("cluster-wide secret reader missing %s: %s", want, body)
		}
	}
}

func TestHandleListResources_Secrets_NoAuthPassthrough(t *testing.T) {
	// auth.mode=none: no user on context. Helpers passthrough; the SA's cache
	// RBAC is the only gate. Every cached secret is visible.
	setupFakeCacheForFilterTests(t)

	result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "secrets"})
	if err != nil {
		t.Fatalf("handleListResources: %v", err)
	}
	body := extractText(t, result)
	for _, want := range []string{"alpha-secret", "beta-secret", "gamma-secret"} {
		if !containsName(body, want) {
			t.Errorf("no-auth passthrough missing %s: %s", want, body)
		}
	}
}

func TestHandleGetResource_Secret_DeniedInNamespace(t *testing.T) {
	// alice has namespace access to alpha and to the alpha namespace's
	// other resources, but the per-namespace secret get SAR denies. The
	// cache has alpha-secret; without the gate this would 200 with the
	// secret object.
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})
	seedSecretGetCanI(t, "alice", nil, []string{"alpha"})

	_, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "secrets", Namespace: "alpha", Name: "alpha-secret"})
	if err == nil {
		t.Fatal("expected forbidden error for secret in denied-secret namespace, got nil")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected 'forbidden' in error, got: %v", err)
	}
}

func TestHandleGetResource_Secret_AllowedInNamespace(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})
	seedSecretGetCanI(t, "alice", []string{"alpha"}, nil)

	result, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "secrets", Namespace: "alpha", Name: "alpha-secret"})
	if err != nil {
		t.Fatalf("handleGetResource: %v", err)
	}
	body := extractText(t, result)
	if !containsName(body, "alpha-secret") {
		t.Errorf("expected alpha-secret in result, got: %s", body)
	}
}

func TestHandleSearch_Secrets_PerNamespaceFanout(t *testing.T) {
	// /api/search (and the MCP equivalent) must do per-namespace SAR fanout
	// for Secrets so the resource browser and search agree on visibility.
	// A cluster-scope SAR alone would silently drop Secret for any
	// namespace-bounded user. Pin: secrets in allowed namespaces are
	// searchable; those in denied namespaces are not.
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha", "beta"})
	seedSecretListCanI(t, "alice", []string{"alpha"}, []string{"beta"})

	result, _, err := handleSearch(ctx, nil, searchInput{Query: "kind:Secret"})
	if err != nil {
		t.Fatalf("handleSearch: %v", err)
	}
	body := extractText(t, result)
	if !containsName(body, "alpha-secret") {
		t.Errorf("expected alpha-secret in search hits: %s", body)
	}
	if containsName(body, "beta-secret") {
		t.Errorf("beta-secret leaked in search despite per-ns deny: %s", body)
	}
}

func TestHandleSearch_Secrets_ClusterWideShape_NsFilter(t *testing.T) {
	// Regression for the bug where AllowedNamespaces==nil (cluster-wide
	// namespace sentinel) plus a concrete `ns:` filter took the cluster-
	// scope SAR branch and skipped Secret entirely. The user has list-pods
	// cluster-wide but secrets only in alpha — the search-RBAC decision
	// must fan out over the requested namespaces, not run a cluster-scope
	// `list secrets` SAR.
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "broad-reader")
	perms := getPermCache().Get("broad-reader")
	if perms == nil {
		t.Fatalf("broad-reader not in cache")
	}
	// No cluster-scope secrets RBAC — only per-namespace in alpha.
	perms.SetCanI("list", "", "secrets", "", false)
	perms.SetCanI("list", "", "secrets", "alpha", true)
	perms.SetCanI("list", "", "secrets", "beta", false)

	result, _, err := handleSearch(ctx, nil, searchInput{Query: "kind:Secret ns:alpha"})
	if err != nil {
		t.Fatalf("handleSearch: %v", err)
	}
	body := extractText(t, result)
	if !containsName(body, "alpha-secret") {
		t.Errorf("expected alpha-secret in search hits (cluster-wide-shape user with per-ns secret RBAC): %s", body)
	}
	if containsName(body, "beta-secret") {
		t.Errorf("beta-secret leaked despite ns:alpha filter + per-ns RBAC: %s", body)
	}
}

func TestNormalizeWorkloadLogsKind_DefaultsToDeployment(t *testing.T) {
	if got := normalizeWorkloadLogsKind(""); got != "deployments" {
		t.Fatalf("blank workload-log kind = %q, want deployments", got)
	}
	if got := normalizeWorkloadLogsKind("statefulset"); got != "statefulsets" {
		t.Fatalf("statefulset workload-log kind = %q, want statefulsets", got)
	}
	if got := normalizeWorkloadLogsKind("job"); got != "jobs" {
		t.Fatalf("job workload-log kind = %q, want jobs", got)
	}
	if got := normalizeWorkloadLogsKind("Workflow"); got != "workflows" {
		t.Fatalf("Workflow workload-log kind = %q, want workflows", got)
	}
}

func TestParseLogsSince(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantSecs  int64
		wantNil   bool
		wantError bool
	}{
		{name: "empty returns nil", in: "", wantNil: true},
		{name: "whitespace returns nil", in: "  ", wantNil: true},
		{name: "30s", in: "30s", wantSecs: 30},
		{name: "10m", in: "10m", wantSecs: 600},
		{name: "1h", in: "1h", wantSecs: 3600},
		{name: "sub-second floors to 1s", in: "500ms", wantSecs: 1},
		{name: "invalid format", in: "10minutes", wantError: true},
		{name: "negative duration", in: "-5m", wantError: true},
		{name: "zero duration", in: "0s", wantError: true},
		{name: "junk", in: "soon", wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLogsSince(tc.in)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected error, got nil (result=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %d", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %d, got nil", tc.wantSecs)
			}
			if *got != tc.wantSecs {
				t.Fatalf("got %d, want %d", *got, tc.wantSecs)
			}
		})
	}
}
