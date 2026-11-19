package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
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
	perms := getPermCache().Get("alice", nil)
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
	perms := getPermCache().Get("bob", nil)
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
// actual variants are EC2NodeClass / AKSNodeClass / GCENodeClass.
func makeNodeClassNode(name string) *topology.Node {
	return &topology.Node{
		ID:     "nodeclass/" + name,
		Kind:   topology.KindNodeClass,
		Name:   name,
		Status: topology.StatusHealthy,
		Data: map[string]any{
			// No namespace — NodeClass is cluster-scoped.
			"apiVersion": "karpenter.k8s.aws/v1",
			"resource":   "ec2nodeclasses",
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
	perms := getPermCache().Get("alice", nil)
	// Deny all NodeClass variants. The helper iterates the table; without
	// discovery only ec2 enters the SAR loop (group != "" check is harmless
	// — the discovery filter is skip-when-missing).
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)

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
	perms := getPermCache().Get("bob", nil)
	// Bob has EC2 access only — should still pass for NodeClass.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)

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
	perms := getPermCache().Get("bob", nil)
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)

	aks := &topology.Node{
		ID:     "nodeclass/aks-default",
		Kind:   topology.KindNodeClass,
		Name:   "aks-default",
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"apiVersion": "karpenter.azure.com/v1beta1",
			"resource":   "aksnodeclasses",
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
			"resource":   "ec2nodeclasses",
		},
	}
	if !canReadNeighborhoodNodeMCP(ctx, ec2) {
		t.Error("EC2 NodeClass denied to user with EC2 RBAC — per-variant gate must allow the matching provider")
	}
}

func TestApplyClusterScopedTopologyRBACMCPFiltersExactNodeClassProvider(t *testing.T) {
	ctx := withTestUserPerms(t, "topology-reader", nil, nil)
	perms := getPermCache().Get("topology-reader", nil)
	perms.SetCanI("list", "eks.amazonaws.com", "nodeclasses", "", true)
	perms.SetCanI("list", "infra.example.io", "customnodeclasses", "", false)

	eksID := "nodeclass//shared/eks.amazonaws.com/nodeclass"
	customID := "nodeclass//shared/infra.example.io/customnodeclass"
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: eksID, Kind: topology.KindNodeClass, Name: "shared", Data: map[string]any{"apiVersion": "eks.amazonaws.com/v1", "resource": "nodeclasses"}},
			{ID: customID, Kind: topology.KindNodeClass, Name: "shared", Data: map[string]any{"apiVersion": "infra.example.io/v1", "resource": "customnodeclasses"}},
		},
	}

	applyClusterScopedTopologyRBAC(ctx, topo)
	if len(topo.Nodes) != 1 || topo.Nodes[0].ID != eksID {
		t.Fatalf("nodes = %+v, want authorized EKS NodeClass only", topo.Nodes)
	}
}

func TestApplyClusterScopedTopologyRBACMCPFiltersCalicoByExactGroup(t *testing.T) {
	ctx := withTestUserPerms(t, "topology-reader", nil, nil)
	perms := getPermCache().Get("topology-reader", nil)
	perms.SetCanI("list", "projectcalico.org", "globalnetworkpolicies", "", true)
	perms.SetCanI("list", "crd.projectcalico.org", "globalnetworkpolicies", "", false)

	projectID := "calicoglobalnetworkpolicy//shared/projectcalico.org"
	legacyID := "calicoglobalnetworkpolicy//shared/crd.projectcalico.org"
	nativeID := "networkpolicy/demo/native"
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: projectID, Kind: topology.KindCalicoGlobalNetworkPolicy, Name: "shared", Data: map[string]any{"apiVersion": "projectcalico.org/v3"}},
			{ID: legacyID, Kind: topology.KindCalicoGlobalNetworkPolicy, Name: "shared", Data: map[string]any{"apiVersion": "crd.projectcalico.org/v1"}},
			{ID: nativeID, Kind: topology.KindNetworkPolicy, Name: "native", Data: map[string]any{"namespace": "demo", "apiVersion": "networking.k8s.io/v1"}},
		},
	}

	applyClusterScopedTopologyRBAC(ctx, topo)
	if len(topo.Nodes) != 2 {
		t.Fatalf("nodes = %+v, want project Calico policy and native NetworkPolicy", topo.Nodes)
	}
	for _, node := range topo.Nodes {
		if node.ID == legacyID {
			t.Fatal("crd.projectcalico.org policy survived exact MCP topology filtering")
		}
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

// Unqualified NodeClass preflight defers to the exact node gate because the
// provider API is open-ended and cannot be represented by a static table.
func TestAllowPseudoKindTuplesMCP_NodeClass_DefersToExactNode(t *testing.T) {
	ctx := withTestUserPerms(t, "alice", nil, nil)
	perms := getPermCache().Get("alice", nil)
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)

	tuples, fallthroughAllow := pseudoKindTuplesForTestMCP("nodeclass", "")
	if len(tuples) != 0 || !fallthroughAllow {
		t.Fatalf("NodeClass preflight must defer: tuples=%+v fallthroughAllow=%v", tuples, fallthroughAllow)
	}
	if !allowPseudoKindTuplesMCP(ctx, tuples, fallthroughAllow) {
		t.Error("NodeClass preflight did not defer to exact per-node authorization")
	}
}

func TestAllowPseudoKindTuplesMCP_NodeClass_AllowedWithProviderSAR(t *testing.T) {
	ctx := withTestUserPerms(t, "bob", nil, nil)
	perms := getPermCache().Get("bob", nil)
	// EC2 only — single-provider grant must be enough for the kind-level gate.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)

	tuples, fallthroughAllow := pseudoKindTuplesForTestMCP("nodeclass", "")
	if !allowPseudoKindTuplesMCP(ctx, tuples, fallthroughAllow) {
		t.Error("nodeclass root preflight denied user with EC2 get-SAR — single-provider RBAC must pass")
	}
}

// Unqualified NodeClass roots defer provider authorization until the concrete
// topology node resolves its API group and resource.
func TestHandleGetNeighborhoodMCP_NodeClassRootNotNamespaceRequired(t *testing.T) {
	setupSecretRefCacheMCP(t)
	withWatchedNeighborhoodKindsMCP(t)

	// Unauthorized
	ctxDeny := withTestUserPerms(t, "alice", nil, nil)
	denyPerms := getPermCache().Get("alice", nil)
	denyPerms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	denyPerms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	denyPerms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)
	_, _, err := handleGetNeighborhood(ctxDeny, nil, getNeighborhoodInput{
		Kind: "nodeclass",
		Name: "foo",
	})
	if err == nil {
		t.Fatal("missing nodeclass unexpectedly returned a result")
	}
	if strings.Contains(err.Error(), "namespace is required") {
		t.Errorf("unqualified nodeclass was misclassified as namespaced: %q", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unauthorized missing nodeclass error = %q, want 'not found' substring", err)
	}

	// Authorized
	ctxAllow := withTestUserPerms(t, "bob", nil, nil)
	allowPerms := getPermCache().Get("bob", nil)
	allowPerms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	allowPerms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	allowPerms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)
	_, _, err2 := handleGetNeighborhood(ctxAllow, nil, getNeighborhoodInput{
		Kind: "nodeclass",
		Name: "foo",
	})
	if err2 == nil {
		t.Fatal("authorized nodeclass call succeeded for nonexistent node — expected 'not found' error from empty subgraph")
	}
	if strings.Contains(err2.Error(), "namespace is required") {
		t.Errorf("unqualified nodeclass was misclassified as namespaced: %q", err2)
	}
	if !strings.Contains(err2.Error(), "not found") {
		t.Errorf("authorized nodeclass error = %q, want 'not found' substring (preflight passed, BFS empty)", err2)
	}
}

// NodePool roots are cluster-scoped and never require a namespace.
func TestHandleGetNeighborhoodMCP_NodePoolRootNotNamespaceRequired(t *testing.T) {
	setupSecretRefCacheMCP(t)
	withWatchedNeighborhoodKindsMCP(t)

	ctxDeny := withTestUserPerms(t, "alice", nil, nil)
	denyPerms := getPermCache().Get("alice", nil)
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
	allowPerms := getPermCache().Get("bob", nil)
	allowPerms.SetCanI("get", "karpenter.sh", "nodepools", "", true)
	_, _, err2 := handleGetNeighborhood(ctxAllow, nil, getNeighborhoodInput{
		Kind: "nodepool",
		Name: "foo",
	})
	if err2 == nil {
		t.Fatal("authorized nodepool call succeeded for nonexistent node — expected 'not found'")
	}
	if strings.Contains(err2.Error(), "namespace is required") {
		t.Errorf("nodepool was misclassified as namespaced: %q", err2)
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
	withNeighborhoodDiscoveryMCP(t, neighborhoodTestDiscoveryMCP())
}

// neighborhoodTestDiscoveryMCP is what a connected cluster's discovery serves
// for the neighborhood fixtures. The neighborhood refuses to answer until
// discovery has served something.
func neighborhoodTestDiscoveryMCP() []*metav1.APIResourceList {
	verbs := metav1.Verbs{"delete", "get", "list", "watch"}
	return []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: verbs},
			{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: verbs},
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: verbs},
			{Name: "services", Kind: "Service", Namespaced: true, Verbs: verbs},
			{Name: "namespaces", Kind: "Namespace", Verbs: verbs},
			{Name: "nodes", Kind: "Node", Verbs: verbs},
		}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
			{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: verbs},
			{Name: "replicasets", Kind: "ReplicaSet", Namespaced: true, Verbs: verbs},
		}},
		{GroupVersion: "karpenter.sh/v1", APIResources: []metav1.APIResource{
			{Name: "nodepools", Kind: "NodePool", Verbs: verbs},
			{Name: "nodeclaims", Kind: "NodeClaim", Verbs: verbs},
		}},
		{GroupVersion: "karpenter.k8s.aws/v1", APIResources: []metav1.APIResource{{Name: "ec2nodeclasses", Kind: "EC2NodeClass", Verbs: verbs}}},
		{GroupVersion: "karpenter.azure.com/v1beta1", APIResources: []metav1.APIResource{{Name: "aksnodeclasses", Kind: "AKSNodeClass", Verbs: verbs}}},
		{GroupVersion: "karpenter.k8s.gcp/v1alpha1", APIResources: []metav1.APIResource{{Name: "gcenodeclasses", Kind: "GCENodeClass", Verbs: verbs}}},
	}
}

func withNeighborhoodDiscoveryMCP(t *testing.T, resources []*metav1.APIResourceList) {
	t.Helper()
	fd := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fd.Resources = resources
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
}

// withWatchedNeighborhoodKindsMCP installs discovery serving the neighborhood
// fixtures' kinds and a dynamic cache that has loaded the custom ones, so an
// absent custom root reads as absent rather than as not collected.
func withWatchedNeighborhoodKindsMCP(t *testing.T) {
	t.Helper()
	var resources []k8s.APIResource
	listKinds := map[schema.GroupVersionResource]string{}
	for _, list := range neighborhoodTestDiscoveryMCP() {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			t.Fatal(err)
		}
		custom := gv.Group != "" && gv.Group != "apps"
		for _, r := range list.APIResources {
			resources = append(resources, k8s.APIResource{Group: gv.Group, Version: gv.Version, Kind: r.Kind, Name: r.Name, Namespaced: r.Namespaced, IsCRD: custom, Verbs: []string(r.Verbs)})
			if custom {
				listKinds[gv.WithResource(r.Name)] = r.Kind + "List"
			}
		}
	}
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds), resources); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	for gvr := range listKinds {
		if err := dc.EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
		if !dc.WaitForSync(gvr, 5*time.Second) {
			t.Fatalf("%s did not sync", gvr)
		}
	}
}

// A Role needs the caller's own permission whether or not discovery lists the
// RBAC group, since the typed cache reads it either way; and before discovery
// has served anything the neighborhood doesn't answer at all.
func TestNeighborhoodMCP_RoleRootNeedsItsOwnPermissionWithoutDiscovery(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "web-role", Namespace: "shop", UID: "role-uid"}},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	t.Cleanup(func() {
		k8s.ResetTestState()
		getPermCache().Invalidate()
	})
	call := func(user string, grant bool) (*mcp.CallToolResult, error) {
		t.Helper()
		ctx := withRestrictedUser(t, user, []string{"shop"})
		getPermCache().Get(user, nil).SetCanI("get", "rbac.authorization.k8s.io", "roles", "shop", grant)
		result, _, err := handleGetNeighborhood(ctx, nil, getNeighborhoodInput{Kind: "role", Namespace: "shop", Name: "web-role"})
		return result, err
	}
	t.Run("rbac group missing from discovery", func(t *testing.T) {
		withNeighborhoodDiscoveryMCP(t, neighborhoodTestDiscoveryMCP())
		if _, err := call("role-granted", true); err != nil {
			t.Fatalf("user who may get roles: %v; the fixture no longer reaches the Role", err)
		}
		if result, err := call("role-denied", false); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("user who may not get roles: %+v, %v; want not found", result, err)
		}
	})
	t.Run("empty discovery", func(t *testing.T) {
		withNeighborhoodDiscoveryMCP(t, nil)
		for _, grant := range []bool{false, true} {
			_, err := call(fmt.Sprintf("role-empty-%v", grant), grant)
			if code, ok := readErrorCodeOf(err); !ok || code != readreason.ClusterConnecting {
				t.Errorf("get roles %v with nothing discovered: %v, want %s", grant, err, readreason.ClusterConnecting)
			}
		}
	})
}

func TestNeighborhoodMCP_BuiltInAPIGroup(t *testing.T) {
	setupSecretRefCacheMCP(t)
	ctx := withTestUserPerms(t, "reader", nil, []string{"default"})
	for _, kind := range []string{"deployment", "Deployment", "deployments"} {
		for _, group := range []string{"", "apps", "wrong.example"} {
			t.Run(kind+"/"+group, func(t *testing.T) {
				call, _, err := handleGetNeighborhood(ctx, nil, getNeighborhoodInput{
					Kind: kind, Group: group, Namespace: "default", Name: "nginx", Hops: 2,
				})
				if group == "wrong.example" {
					if err == nil || !strings.Contains(err.Error(), "not found") {
						t.Fatalf("wrong group: expected not found, got %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var result neighborhoodResult
				if err := json.Unmarshal([]byte(call.Content[0].(*mcp.TextContent).Text), &result); err != nil {
					t.Fatal(err)
				}
				if result.Root.Kind != "Deployment" || len(result.Subgraph.Nodes) == 0 || result.Subgraph.Nodes[0].Data["apiVersion"] != "apps/v1" {
					t.Fatalf("wrong root: %+v", result)
				}
			})
		}
	}
	denied := withTestUserPerms(t, "denied", nil, []string{"other"})
	if _, _, err := handleGetNeighborhood(denied, nil, getNeighborhoodInput{
		Kind: "deployment", Group: "apps", Namespace: "default", Name: "nginx",
	}); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("denied namespace should remain forbidden, got %v", err)
	}
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
	bobPerms := getPermCache().Get("bob", nil)
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
	alicePerms := getPermCache().Get("alice", nil)
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

// A root Radar doesn't collect answers with the read's own code, so an agent
// can tell it from one that doesn't exist; a caller who may not read the root
// still gets not found.
func TestNeighborhoodMCP_UncollectedRootAnswersWithItsReadCode(t *testing.T) {
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "shop"}},
	), map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true}, k8score.Namespaces: {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	t.Cleanup(func() {
		k8s.ResetTestState()
		getPermCache().Invalidate()
	})
	withNeighborhoodDiscoveryMCP(t, neighborhoodTestDiscoveryMCP())
	for _, grant := range []bool{true, false} {
		user := fmt.Sprintf("secret-reader-%v", grant)
		ctx := withRestrictedUser(t, user, []string{"shop"})
		getPermCache().Get(user, nil).SetCanI("get", "", "secrets", "shop", grant)
		_, _, err := handleGetNeighborhood(ctx, nil, getNeighborhoodInput{Kind: "secret", Namespace: "shop", Name: "key"})
		code, coded := readErrorCodeOf(err)
		switch {
		case grant && code != readreason.RadarKindNotCollected:
			t.Errorf("may get secrets: %v, want %s", err, readreason.RadarKindNotCollected)
		case !grant && (coded || err == nil || !strings.Contains(err.Error(), "not found")):
			t.Errorf("may not get secrets: %v, want not found", err)
		}
	}
}

// A walk that runs out of time says so and how to narrow the request.
func TestNeighborhoodMCP_PastDeadlineSaysHowToNarrow(t *testing.T) {
	setupSecretRefCacheMCP(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, _, err := handleGetNeighborhood(ctx, nil, getNeighborhoodInput{Kind: "deployment", Namespace: "default", Name: "nginx", Hops: 2})
	if err == nil || !strings.Contains(err.Error(), "deadline") || !strings.Contains(err.Error(), "reduce hops (current 2)") {
		t.Fatalf("past-deadline walk: %v", err)
	}
}

// A root whose kind Radar may not read or doesn't collect says so with the
// read's code, and never suggests a same-named object of another kind.
func TestNeighborhoodMCP_RootKindDeniedToRadarSaysSo(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "bookinfo"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "reviews", Namespace: "bookinfo"}},
	)); err != nil {
		t.Fatal(err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	t.Cleanup(func() {
		k8s.ResetTestState()
		getPermCache().Invalidate()
	})
	gvr := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
	// Unwatched, the neighborhood starts the kind's watch itself, whose access
	// check finds the denial.
	for _, tc := range []struct {
		watch bool
		want  readreason.Code
	}{{true, readreason.RadarForbidden}, {false, readreason.RadarForbidden}} {
		t.Run(fmt.Sprintf("watched=%v", tc.watch), func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "VirtualServiceList"})
			dyn.PrependReactor("list", "virtualservices", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("radar may not list virtualservices"))
			})
			verbs := []string{"delete", "get", "list", "watch"}
			k8s.ResetTestDynamicState()
			t.Cleanup(k8s.ResetTestDynamicState)
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
				{Version: "v1", Kind: "Service", Name: "services", Namespaced: true, Verbs: verbs},
				{Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: verbs},
				{Group: gvr.Group, Version: gvr.Version, Kind: "VirtualService", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: verbs},
			}); err != nil {
				t.Fatal(err)
			}
			if tc.watch {
				dc := k8s.GetDynamicResourceCache()
				_ = dc.EnsureWatching(gvr)
				for deadline := time.Now().Add(5 * time.Second); dc.Observation(gvr).State != k8score.DynamicObservationDenied; time.Sleep(10 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatalf("the denial was never observed: %+v", dc.Observation(gvr))
					}
				}
			}
			ctx := withRestrictedUser(t, "istio-reader", []string{"bookinfo"})
			_, _, err := handleGetNeighborhood(ctx, nil, getNeighborhoodInput{Kind: "virtualservice", Group: "networking.istio.io", Namespace: "bookinfo", Name: "reviews", Hops: 2})
			if code, _ := readErrorCodeOf(err); code != tc.want || strings.Contains(err.Error(), "retry with kind") || strings.Contains(err.Error(), "not found") {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

// A root whose custom kind nothing has watched starts the kind's watch, as a
// read of the object would, and answers from it once it loads; until then it
// says the kind is still loading.
func TestNeighborhoodMCP_UnwatchedCustomRootStartsItsWatch(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}})); err != nil {
		t.Fatal(err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	t.Cleanup(func() {
		k8s.ResetTestState()
		getPermCache().Invalidate()
	})
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, hold := range []bool{false, true} {
		t.Run(fmt.Sprintf("held=%v", hold), func(t *testing.T) {
			widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "w1", "namespace": "shop", "uid": "w1"}}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, widget)
			verbs := []string{"delete", "get", "list", "watch"}
			k8s.ResetTestDynamicState()
			t.Cleanup(k8s.ResetTestDynamicState)
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
				{Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: verbs},
				{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: verbs},
			}); err != nil {
				t.Fatal(err)
			}
			if hold {
				dyn.PrependReactor("list", "widgets", func(a k8stesting.Action) (bool, runtime.Object, error) {
					if l, ok := a.(k8stesting.ListActionImpl); ok && l.ListOptions.Limit == 1 {
						return false, nil, nil
					}
					return true, nil, apierrors.NewInternalError(fmt.Errorf("the API server is busy"))
				})
			}
			ctx := withRestrictedUser(t, "widget-reader", []string{"shop"})
			result, _, err := handleGetNeighborhood(ctx, nil, getNeighborhoodInput{Kind: "widget", Group: "example.io", Namespace: "shop", Name: "w1"})
			if hold {
				if code, _ := readErrorCodeOf(err); code != readreason.KindSyncPending {
					t.Fatalf("got %v, want %s", err, readreason.KindSyncPending)
				}
				return
			}
			if err != nil || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, `"name":"w1"`) {
				t.Fatalf("got %v %v, want the Widget's neighborhood", result, err)
			}
		})
	}
}

// A neighborhood cut off by its deadline is logged as a timeout, as REST logs
// none, not as a failed call carrying its error text.
func TestNeighborhoodMCP_TimeoutIsNotLoggedAsAnError(t *testing.T) {
	setupSecretRefCacheMCP(t)
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	call := logToolCall("get_neighborhood", handleGetNeighborhood)
	if _, _, err := call(ctx, nil, getNeighborhoodInput{Kind: "deployment", Namespace: "default", Name: "nginx"}); err == nil {
		t.Fatal("a walk past its deadline answered")
	}
	out := logs.String()
	if !strings.Contains(out, "TIMEOUT") || !strings.Contains(out, "level=warn component=mcp tool=get_neighborhood") || strings.Contains(out, "level=error") || strings.Contains(out, "narrow it") {
		t.Fatalf("timeout logged as a failure:\n%s", out)
	}
}
