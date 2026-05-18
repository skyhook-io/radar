package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/topology"
)

// canReadNeighborhoodNodeMCP must apply per-kind Secret RBAC inside an allowed
// namespace. Mirrors handleGetResource: namespace access alone is NOT a
// sufficient gate for Secrets because the SA backing the cache may carry
// cluster-wide secrets RBAC (Helm release visibility) the calling user lacks.

func makeSecretNode(ns, name string) *topology.Node {
	return &topology.Node{
		ID:     "secret/" + ns + "/" + name,
		Kind:   topology.KindSecret,
		Name:   name,
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"namespace":  ns,
			"apiVersion": "v1",
		},
	}
}

func makeConfigMapNode(ns, name string) *topology.Node {
	return &topology.Node{
		ID:     "configmap/" + ns + "/" + name,
		Kind:   topology.KindConfigMap,
		Name:   name,
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"namespace":  ns,
			"apiVersion": "v1",
		},
	}
}

// TestCanReadNeighborhoodNodeMCP_SecretRequiresPerKindRBAC pins the gate:
// a user with namespace access but no per-namespace `get secrets` SAR must be
// denied. The cache may hold Secrets the user can't read directly — the
// neighborhood graph must not leak them.
func TestCanReadNeighborhoodNodeMCP_SecretRequiresPerKindRBAC(t *testing.T) {
	ctx := withTestUserPerms(t, "alice", nil, []string{"default"})
	// alice has namespace access to default but explicit deny on secrets-get.
	perms := getPermCache().Get("alice")
	perms.SetCanI("get", "", "secrets", "default", false)

	secret := makeSecretNode("default", "nginx-tls")
	if canReadNeighborhoodNodeMCP(ctx, secret) {
		t.Error("Secret node leaked through MCP neighborhood gate: namespace access without per-kind RBAC must deny")
	}
}

// Counterpart: user WITH namespace access AND per-namespace secrets-get
// must be allowed. Locks down the positive path so the gate isn't blanket-deny.
func TestCanReadNeighborhoodNodeMCP_SecretAllowedWithPerKindRBAC(t *testing.T) {
	ctx := withTestUserPerms(t, "bob", nil, []string{"default"})
	perms := getPermCache().Get("bob")
	perms.SetCanI("get", "", "secrets", "default", true)

	secret := makeSecretNode("default", "nginx-tls")
	if !canReadNeighborhoodNodeMCP(ctx, secret) {
		t.Error("authorized user denied: namespace access + per-kind RBAC should pass")
	}
}

// Sanity: non-Secret namespaced kinds (e.g. ConfigMap) ride on the namespace
// gate alone. The Secret-specific tightening must not regress that.
func TestCanReadNeighborhoodNodeMCP_ConfigMapStaysOnNamespaceGate(t *testing.T) {
	ctx := withTestUserPerms(t, "alice", nil, []string{"default"})
	// No configmap-specific SAR seeded — namespace access alone should pass
	// because we deliberately do NOT tighten that for ConfigMap.
	cm := makeConfigMapNode("default", "nginx-conf")
	if !canReadNeighborhoodNodeMCP(ctx, cm) {
		t.Error("ConfigMap node denied: namespace access should be sufficient (no per-kind tightening for ConfigMap)")
	}
}

// Smoke: no-auth callers (no user in context) pass through. Matches the
// passthrough behavior of every per-namespace RBAC helper in this package.
func TestCanReadNeighborhoodNodeMCP_NoAuthPassthrough(t *testing.T) {
	// Empty context — no user attached. Helpers should not deny.
	secret := makeSecretNode("default", "nginx-tls")
	if !canReadNeighborhoodNodeMCP(pkgauth.ContextWithUser(t.Context(), nil), secret) {
		t.Error("no-auth caller denied — Secret gate must not fail-closed when auth is disabled")
	}
}

// makeNodeClassNode builds a topology pseudo-kind NodeClass node. The Kind is
// the synthesized topology label ("NodeClass"), not a real K8s resource — the
// actual variants are EC2NodeClass / AKSNodeClass / GCPNodeClass.
func makeNodeClassNode(name string) *topology.Node {
	return &topology.Node{
		ID:     "nodeclass/" + name,
		Kind:   topology.KindNodeClass,
		Name:   name,
		Status: topology.StatusHealthy,
		Data: map[string]any{
			// No namespace — NodeClass is cluster-scoped.
			"apiVersion": "karpenter.k8s.aws/v1",
		},
	}
}

func makeKnativeServiceNode(ns, name string) *topology.Node {
	return &topology.Node{
		ID:     "knativeservice/" + ns + "/" + name,
		Kind:   topology.KindKnativeService,
		Name:   name,
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"namespace":  ns,
			"apiVersion": "serving.knative.dev/v1",
		},
	}
}

// TestCanReadNeighborhoodNodeMCP_NodeClassRequiresPerProviderSAR pins the
// pseudo-kind cluster-scoped fix: NodeClass is a topology-only label that
// ClassifyKindScope doesn't recognize. Without the clusterScopedTopologyKinds
// lookup, NodeClass nodes hit the unclassified+empty-namespace allow branch
// and surface to users without provider-specific RBAC.
func TestCanReadNeighborhoodNodeMCP_NodeClassDeniedWithoutSAR(t *testing.T) {
	ctx := withTestUserPerms(t, "alice", nil, nil)
	perms := getPermCache().Get("alice")
	// Deny all NodeClass variants. The helper iterates the table; without
	// discovery only ec2 enters the SAR loop (group != "" check is harmless
	// — the discovery filter is skip-when-missing).
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)

	n := makeNodeClassNode("default-class")
	if canReadNeighborhoodNodeMCP(ctx, n) {
		t.Error("NodeClass pseudo-kind leaked to user without any provider get-SAR")
	}
}

// Counterpart: user with one provider's get-SAR sees NodeClass nodes. Mirrors
// the topology-strip semantics — denial requires ALL discovery-present
// providers to fail.
func TestCanReadNeighborhoodNodeMCP_NodeClassAllowedWithProviderSAR(t *testing.T) {
	ctx := withTestUserPerms(t, "bob", nil, nil)
	perms := getPermCache().Get("bob")
	// Bob has EC2 access only — should still pass for NodeClass.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)

	n := makeNodeClassNode("default-class")
	if !canReadNeighborhoodNodeMCP(ctx, n) {
		t.Error("NodeClass denied for user with EC2 get-SAR — single-provider RBAC must allow")
	}
}

// TestCanReadNeighborhoodNodeMCP_NodeClassPerVariantDeniesWrongProvider
// pins the per-variant authorization fix on the MCP side: a user with
// EC2 RBAC must not see AKS NodeClass nodes. Mirrors the REST test.
func TestCanReadNeighborhoodNodeMCP_NodeClassPerVariantDeniesWrongProvider(t *testing.T) {
	ctx := withTestUserPerms(t, "bob", nil, nil)
	perms := getPermCache().Get("bob")
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)

	aks := &topology.Node{
		ID:     "nodeclass/aks-default",
		Kind:   topology.KindNodeClass,
		Name:   "aks-default",
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"apiVersion": "karpenter.azure.com/v1beta1",
		},
	}
	if canReadNeighborhoodNodeMCP(ctx, aks) {
		t.Error("AKS NodeClass leaked to user with EC2-only RBAC — per-variant gate must deny cross-provider")
	}

	ec2 := &topology.Node{
		ID:     "nodeclass/ec2-default",
		Kind:   topology.KindNodeClass,
		Name:   "ec2-default",
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"apiVersion": "karpenter.k8s.aws/v1",
		},
	}
	if !canReadNeighborhoodNodeMCP(ctx, ec2) {
		t.Error("EC2 NodeClass denied to user with EC2 RBAC — per-variant gate must allow the matching provider")
	}
}

// KnativeService is a namespaced pseudo-kind. The cluster-scoped table
// shouldn't match it; the helper must fall through to the namespaced branch
// and ride on namespace access alone (no per-kind tightening for Knative).
func TestCanReadNeighborhoodNodeMCP_KnativeServiceUsesNamespaceGate(t *testing.T) {
	ctx := withTestUserPerms(t, "alice", nil, []string{"prod"})
	n := makeKnativeServiceNode("prod", "api")
	if !canReadNeighborhoodNodeMCP(ctx, n) {
		t.Error("namespaced pseudo-kind KnativeService denied — namespace access should be sufficient")
	}

	// User without namespace access → denied.
	ctxDenied := withTestUserPerms(t, "carol", nil, []string{"staging"})
	if canReadNeighborhoodNodeMCP(ctxDenied, n) {
		t.Error("KnativeService allowed for user without namespace access — namespace gate must apply")
	}
}

// pseudoKindTuplesForTestMCP mirrors the inline topology.RBACTuplesForKind
// call in handleGetNeighborhood. disc=nil in tests so no rows are filtered.
func pseudoKindTuplesForTestMCP(kind, group string) (tuples []topology.SARTuple, fallthroughAllow bool) {
	t, _, fa := topology.RBACTuplesForKind(kind, group, nil)
	return t, fa
}

// TestAllowPseudoKindTuplesMCP_NodeClass pins the MCP-side root-preflight
// helper: kind-only lookup (no node yet) iterates every table row under that
// kind and allows on any pass. Mirrors the REST test of the same name.
func TestAllowPseudoKindTuplesMCP_NodeClass_DeniedWithoutSAR(t *testing.T) {
	ctx := withTestUserPerms(t, "alice", nil, nil)
	perms := getPermCache().Get("alice")
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)

	tuples, fallthroughAllow := pseudoKindTuplesForTestMCP("nodeclass", "")
	if len(tuples) == 0 {
		t.Fatal("RBACTuplesForKind returned 0 tuples for nodeclass — table wiring is broken")
	}
	if allowPseudoKindTuplesMCP(ctx, tuples, fallthroughAllow) {
		t.Error("nodeclass root preflight allowed user without any provider get-SAR — must deny")
	}
}

func TestAllowPseudoKindTuplesMCP_NodeClass_AllowedWithProviderSAR(t *testing.T) {
	ctx := withTestUserPerms(t, "bob", nil, nil)
	perms := getPermCache().Get("bob")
	// EC2 only — single-provider grant must be enough for the kind-level gate.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)

	tuples, fallthroughAllow := pseudoKindTuplesForTestMCP("nodeclass", "")
	if !allowPseudoKindTuplesMCP(ctx, tuples, fallthroughAllow) {
		t.Error("nodeclass root preflight denied user with EC2 get-SAR — single-provider RBAC must pass")
	}
}

// TestHandleGetNeighborhoodMCP_NodeClassRootNotNamespaceRequired pins the
// integration fix on the MCP surface: an agent calling get_neighborhood with
// kind="nodeclass" and Namespace="" (which is what get_topology output
// suggests) must NOT receive "namespace is required" — that's the bug we're
// closing.
//
// Two cases:
//   - Unauthorized user (no provider SAR): error must mention "forbidden",
//     not "namespace is required".
//   - Authorized user (EC2 SAR): preflight passes, BFS runs against the
//     seeded cache (no NodeClass nodes there), root lookup misses → "not
//     found" error, also not "namespace is required".
func TestHandleGetNeighborhoodMCP_NodeClassRootNotNamespaceRequired(t *testing.T) {
	setupSecretRefCacheMCP(t)

	// Unauthorized
	ctxDeny := withTestUserPerms(t, "alice", nil, nil)
	denyPerms := getPermCache().Get("alice")
	denyPerms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	denyPerms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	denyPerms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)
	_, _, err := handleGetNeighborhood(ctxDeny, nil, getNeighborhoodInput{
		Kind: "nodeclass",
		Name: "foo",
	})
	if err == nil {
		t.Fatal("unauthorized nodeclass call succeeded — preflight must deny")
	}
	if strings.Contains(err.Error(), "namespace is required") {
		t.Errorf("unauthorized nodeclass returned namespace error %q — regression: pseudo-kind misclassified as namespaced", err)
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("unauthorized nodeclass error = %q, want 'forbidden' substring", err)
	}

	// Authorized
	ctxAllow := withTestUserPerms(t, "bob", nil, nil)
	allowPerms := getPermCache().Get("bob")
	allowPerms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	allowPerms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	allowPerms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)
	_, _, err2 := handleGetNeighborhood(ctxAllow, nil, getNeighborhoodInput{
		Kind: "nodeclass",
		Name: "foo",
	})
	if err2 == nil {
		t.Fatal("authorized nodeclass call succeeded for nonexistent node — expected 'not found' error from empty subgraph")
	}
	if strings.Contains(err2.Error(), "namespace is required") {
		t.Errorf("authorized nodeclass returned namespace error %q even WITH EC2 SAR — regression", err2)
	}
	if !strings.Contains(err2.Error(), "not found") {
		t.Errorf("authorized nodeclass error = %q, want 'not found' substring (preflight passed, BFS empty)", err2)
	}
}

// Same shape for NodePool — sanity that the fix covers every cluster-scoped
// pseudo-kind in the table, not just NodeClass.
func TestHandleGetNeighborhoodMCP_NodePoolRootNotNamespaceRequired(t *testing.T) {
	setupSecretRefCacheMCP(t)

	ctxDeny := withTestUserPerms(t, "alice", nil, nil)
	denyPerms := getPermCache().Get("alice")
	denyPerms.SetCanI("get", "karpenter.sh", "nodepools", "", false)
	_, _, err := handleGetNeighborhood(ctxDeny, nil, getNeighborhoodInput{
		Kind: "nodepool",
		Name: "foo",
	})
	if err == nil {
		t.Fatal("unauthorized nodepool call succeeded — preflight must deny")
	}
	if strings.Contains(err.Error(), "namespace is required") {
		t.Errorf("unauthorized nodepool returned namespace error %q — pseudo-kind misclassified", err)
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("unauthorized nodepool error = %q, want 'forbidden' substring", err)
	}

	ctxAllow := withTestUserPerms(t, "bob", nil, nil)
	allowPerms := getPermCache().Get("bob")
	allowPerms.SetCanI("get", "karpenter.sh", "nodepools", "", true)
	_, _, err2 := handleGetNeighborhood(ctxAllow, nil, getNeighborhoodInput{
		Kind: "nodepool",
		Name: "foo",
	})
	if err2 == nil {
		t.Fatal("authorized nodepool call succeeded for nonexistent node — expected 'not found'")
	}
	if strings.Contains(err2.Error(), "namespace is required") {
		t.Errorf("authorized nodepool returned namespace error %q even with SAR — regression", err2)
	}
	if !strings.Contains(err2.Error(), "not found") {
		t.Errorf("authorized nodepool error = %q, want 'not found' substring", err2)
	}
}

// setupSecretRefCacheMCP seeds a fake cache with a Deployment that references
// a Secret via Volumes — the only shape the topology builder uses to decide
// whether to surface the Secret node. Without this reference, IncludeSecrets=
// true alone wouldn't make the Secret appear (see pkg/topology/builder.go's
// "isReferenced" guard in section 9).
func setupSecretRefCacheMCP(t *testing.T) {
	t.Helper()
	replicas := int32(1)
	fakeClient := fake.NewClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "nginx-tls", Namespace: "default"},
			Type:       corev1.SecretTypeOpaque,
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "nginx", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "nginx"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "nginx"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}},
						Volumes: []corev1.Volume{{
							Name: "tls",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: "nginx-tls"},
							},
						}},
					},
				},
			},
		},
	)
	if err := k8s.InitTestResourceCache(fakeClient); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	t.Cleanup(func() {
		k8s.ResetTestState()
		getPermCache().Invalidate()
	})
}

// TestNeighborhoodMCP_SecretRootIncluded pins the IncludeSecrets fix on the
// MCP side. DefaultBuildOptions sets IncludeSecrets=false, so the Secret
// node isn't in the topology and the root lookup returns "resource not
// found" even for authorized users. The handler must override
// IncludeSecrets=true so authorized callers find the root, while the
// per-namespace `get secrets` Allow gate still denies unauthorized callers
// via the empty-subgraph path (preserving existence-hiding).
func TestNeighborhoodMCP_SecretRootIncluded(t *testing.T) {
	setupSecretRefCacheMCP(t)

	// Authorized user: namespace access to default + per-namespace
	// `get secrets`. Both Allow checks must pass; root resolves; result
	// includes the Secret node.
	ctx := withTestUserPerms(t, "bob", nil, []string{"default"})
	bobPerms := getPermCache().Get("bob")
	bobPerms.SetCanI("get", "", "secrets", "default", true)

	call, _, err := handleGetNeighborhood(ctx, nil, getNeighborhoodInput{
		Kind:      "secret",
		Namespace: "default",
		Name:      "nginx-tls",
	})
	if err != nil {
		t.Fatalf("authorized user got error for Secret root: %v — IncludeSecrets override must surface Secret nodes in topology", err)
	}
	if call == nil || len(call.Content) == 0 {
		t.Fatal("expected MCP CallToolResult content for authorized Secret root")
	}
	tc, ok := call.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("unexpected content type: %T", call.Content[0])
	}
	var result neighborhoodResult
	if jsonErr := json.Unmarshal([]byte(tc.Text), &result); jsonErr != nil {
		t.Fatalf("decode MCP result: %v (raw=%q)", jsonErr, tc.Text)
	}
	foundSecret := false
	for _, n := range result.Subgraph.Nodes {
		if n.Kind == topology.KindSecret && n.Name == "nginx-tls" {
			foundSecret = true
			break
		}
	}
	if !foundSecret {
		t.Errorf("authorized user's response missing Secret node — IncludeSecrets override should have surfaced it (nodes=%+v)", result.Subgraph.Nodes)
	}

	// Unauthorized user: namespace access but NO per-namespace `get secrets`
	// SAR. Allow rejects the root → empty subgraph → "not found" error.
	// Existence-hiding preserved: same 404-shape result the
	// IncludeSecrets=false path produced, but driven by RBAC.
	ctxDenied := withTestUserPerms(t, "alice", nil, []string{"default"})
	alicePerms := getPermCache().Get("alice")
	alicePerms.SetCanI("get", "", "secrets", "default", false)

	_, _, err2 := handleGetNeighborhood(ctxDenied, nil, getNeighborhoodInput{
		Kind:      "secret",
		Namespace: "default",
		Name:      "nginx-tls",
	})
	if err2 == nil {
		t.Error("unauthorized user got success for Secret root — Allow gate must produce not-found via empty subgraph (existence-hiding)")
	} else if !strings.Contains(err2.Error(), "not found") {
		t.Errorf("unauthorized user got unexpected error %v — expected 'not found' shape to mirror existence-hiding", err2)
	}
}
