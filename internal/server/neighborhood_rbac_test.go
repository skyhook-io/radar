package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/topology"
)

// neighborhoodTestDiscovery is what a connected cluster's discovery serves for
// the neighborhood fixtures. The neighborhood refuses to answer until
// discovery has served something.
func neighborhoodTestDiscovery() []*metav1.APIResourceList {
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

func withNeighborhoodDiscovery(t *testing.T, resources []*metav1.APIResourceList) {
	t.Helper()
	fd := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fd.Resources = resources
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
}

// withWatchedNeighborhoodKinds installs discovery serving the neighborhood
// fixtures' kinds and a dynamic cache that has loaded the custom ones, so an
// absent custom root reads as absent rather than as not collected.
func withWatchedNeighborhoodKinds(t *testing.T) {
	t.Helper()
	var resources []k8s.APIResource
	listKinds := map[schema.GroupVersionResource]string{}
	for _, list := range neighborhoodTestDiscovery() {
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

// canReadNeighborhoodNode must apply per-kind Secret RBAC inside an allowed
// namespace. Mirrors the gate that handleGetResource already applies: namespace
// access alone is NOT sufficient — the SA backing the cache may have cluster-
// wide secrets RBAC (Helm release visibility) the calling user does not. If the
// per-kind SAR is missing, Secret nodes would leak through the neighborhood BFS
// to users who can't read them directly.

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

// TestCanReadNeighborhoodNode_SecretRequiresPerKindRBAC pins the new gate:
// a user with namespace access but no per-namespace `get secrets` SAR must be
// denied. Same setup as the existing handleGetResource secrets RBAC tests.
func TestCanReadNeighborhoodNode_SecretRequiresPerKindRBAC(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	// alice has namespace access to default but NOT per-namespace secrets-get.
	perms := s.permCache.Get("alice", nil)
	perms.SetCanI("get", "", "secrets", "default", false)

	r := requestWithUser("GET", "/api/ai/neighborhood/pod/default/anything", &auth.User{
		Username: "alice",
	})
	secret := makeSecretNode("default", "nginx-tls")

	if s.canReadNeighborhoodNode(r, secret) {
		t.Error("Secret node leaked through neighborhood gate: namespace access without per-kind RBAC must deny")
	}
}

// Counterpart: a user WITH namespace access AND per-namespace secrets-get
// must be allowed. Locks down the positive path so the gate isn't blanket-deny.
func TestCanReadNeighborhoodNode_SecretAllowedWithPerKindRBAC(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("bob", nil, &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	perms := s.permCache.Get("bob", nil)
	perms.SetCanI("get", "", "secrets", "default", true)

	r := requestWithUser("GET", "/api/ai/neighborhood/pod/default/anything", &auth.User{
		Username: "bob",
	})
	secret := makeSecretNode("default", "nginx-tls")

	if !s.canReadNeighborhoodNode(r, secret) {
		t.Error("authorized user denied: namespace access + per-kind RBAC should pass")
	}
}

// Sanity: non-Secret namespaced kinds (e.g. ConfigMap) ride on the namespace
// gate alone — adding the Secret-specific tightening must not regress that.
func TestCanReadNeighborhoodNode_ConfigMapStaysOnNamespaceGate(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	// alice has no configmap-specific SAR seeded — namespace access alone
	// should pass for ConfigMap because we deliberately do NOT tighten that.
	r := requestWithUser("GET", "/api/ai/neighborhood/pod/default/anything", &auth.User{
		Username: "alice",
	})
	cm := makeConfigMapNode("default", "nginx-conf")

	if !s.canReadNeighborhoodNode(r, cm) {
		t.Error("ConfigMap node denied: namespace access should be sufficient (no per-kind tightening for ConfigMap)")
	}
}

// Roles, RoleBindings, LimitRanges and Trivy reports need the user's own
// permission on the kind, as a resource read does; namespace access alone
// doesn't reveal them.
func TestCanReadNeighborhoodNode_ExactKindsRequirePerKindRBAC(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	for _, tc := range []struct{ kind, apiVersion, resource string }{
		{"Role", "rbac.authorization.k8s.io/v1", "roles"},
		{"RoleBinding", "rbac.authorization.k8s.io/v1", "rolebindings"},
		{"LimitRange", "v1", "limitranges"},
		{"VulnerabilityReport", "aquasecurity.github.io/v1alpha1", "vulnerabilityreports"},
	} {
		node := &topology.Node{ID: tc.resource + "/shop/x", Kind: topology.NodeKind(tc.kind), Name: "x", Data: map[string]any{"namespace": "shop", "apiVersion": tc.apiVersion, "resource": tc.resource}}
		group := ""
		if i := strings.Index(tc.apiVersion, "/"); i > 0 {
			group = tc.apiVersion[:i]
		}
		for _, grant := range []bool{false, true} {
			user := fmt.Sprintf("%s-%v", tc.resource, grant)
			s.permCache.Set(user, nil, &auth.UserPermissions{AllowedNamespaces: []string{"shop"}})
			s.permCache.Get(user, nil).SetCanI("get", group, tc.resource, "shop", grant)
			r := requestWithUser("GET", "/api/ai/neighborhood/replicaset/shop/web", &auth.User{Username: user})
			if got := s.canReadNeighborhoodNode(r, node); got != grant {
				t.Errorf("%s with get %s = %v: allowed = %v", tc.kind, tc.resource, grant, got)
			}
		}
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

// TestCanReadNeighborhoodNode_NodeClassRequiresPerProviderSAR pins the
// pseudo-kind cluster-scoped fix: NodeClass is a topology-only label that
// ClassifyKindScope doesn't recognize. Without the clusterScopedTopologyKinds
// lookup, NodeClass nodes hit the unclassified+empty-namespace allow branch
// and leak to users without provider-specific RBAC.
func TestCanReadNeighborhoodNode_NodeClassDeniedWithoutSAR(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	// Deny all NodeClass variants. The helper iterates the table; entries
	// not in discovery (typical for the test env) are skipped by the
	// discovery filter — so the SARs only fire on what discovery has.
	// disc=nil in tests → no entries filtered out → all 3 SARs run.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)
	s.permCache.Set("alice", nil, perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "alice"})
	n := makeNodeClassNode("default-class")
	if s.canReadNeighborhoodNode(r, n) {
		t.Error("NodeClass pseudo-kind leaked to user without any provider get-SAR")
	}
}

// Counterpart: user with one provider's get-SAR sees NodeClass nodes. Mirrors
// the topology-strip semantics — denial requires ALL discovery-present
// providers to fail; a single allow is sufficient.
func TestCanReadNeighborhoodNode_NodeClassAllowedWithProviderSAR(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	// Bob has EC2 access only — should still pass for NodeClass.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)
	s.permCache.Set("bob", nil, perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "bob"})
	n := makeNodeClassNode("default-class")
	if !s.canReadNeighborhoodNode(r, n) {
		t.Error("NodeClass denied for user with EC2 get-SAR — single-provider RBAC must allow")
	}
}

// TestCanReadNeighborhoodNode_NodeClassPerVariantSAR pins the per-variant
// authorization fix: NodeClass has 3 entries in ClusterScopedKinds (EC2 /
// AKS / GCP). Before the fix, the helper iterated ALL entries and returned
// true on the FIRST passing SAR — so a user with EC2 RBAC saw AKS and GCP
// NodeClass nodes too. The fix matches the table row by BOTH Kind and the
// node's apiVersion-group, so an AKS NodeClass node is SARed against the
// AKS row only.
//
// Setup: Bob has EC2 RBAC only. An AKS NodeClass node (apiVersion=
// karpenter.azure.com/v1beta1) must be denied — his EC2 grant must not
// leak across providers.
func TestCanReadNeighborhoodNode_NodeClassPerVariantDeniesWrongProvider(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)
	s.permCache.Set("bob", nil, perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "bob"})

	// AKS NodeClass node — apiVersion-group is karpenter.azure.com. The
	// per-variant lookup must SAR ONLY the AKS row, which is denied.
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
	if s.canReadNeighborhoodNode(r, aks) {
		t.Error("AKS NodeClass leaked to user with EC2-only RBAC — per-variant gate must deny cross-provider")
	}

	// Sanity counterpart: same user, EC2 NodeClass — must allow.
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
	if !s.canReadNeighborhoodNode(r, ec2) {
		t.Error("EC2 NodeClass denied to user with EC2 RBAC — per-variant gate must allow the matching provider")
	}
}

// KnativeService is a namespaced pseudo-kind. The cluster-scoped table
// shouldn't match it; the helper must fall through to the namespaced branch
// and ride on namespace access alone (no per-kind tightening for Knative).
func TestCanReadNeighborhoodNode_KnativeServiceUsesNamespaceGate(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"prod"}})

	r := requestWithUser("GET", "/api/ai/neighborhood/service/prod/api", &auth.User{Username: "alice"})
	n := makeKnativeServiceNode("prod", "api")
	if !s.canReadNeighborhoodNode(r, n) {
		t.Error("namespaced pseudo-kind KnativeService denied — namespace access should be sufficient")
	}

	// Sanity: user without namespace access → denied.
	s.permCache.Set("carol", nil, &auth.UserPermissions{AllowedNamespaces: []string{"staging"}})
	r2 := requestWithUser("GET", "/api/ai/neighborhood/service/prod/api", &auth.User{Username: "carol"})
	if s.canReadNeighborhoodNode(r2, n) {
		t.Error("KnativeService allowed for user without namespace access — namespace gate must apply")
	}
}

// pseudoKindTuplesForTest is the test analogue of the inline
// topology.RBACTuplesForKind call in handleAINeighborhood — disc=nil in
// tests, so no rows are filtered, exactly matching the production path with
// discovery offline.
func pseudoKindTuplesForTest(kind, group string) (tuples []topology.SARTuple, fallthroughAllow bool) {
	t, _, fa := topology.RBACTuplesForKind(kind, group, nil)
	return t, fa
}

// An unqualified NodeClass root cannot be authorized from a finite provider
// table. Preflight defers to the exact node gate, which fails closed on the
// concrete group/resource after root resolution.
func TestAllowPseudoKindTuples_NodeClass_DefersToExactNode(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)
	s.permCache.Set("alice", nil, perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "alice"})
	tuples, fallthroughAllow := pseudoKindTuplesForTest("nodeclass", "")
	if len(tuples) != 0 || !fallthroughAllow {
		t.Fatalf("NodeClass preflight must defer: tuples=%+v fallthroughAllow=%v", tuples, fallthroughAllow)
	}
	if !s.allowPseudoKindTuples(r, tuples, fallthroughAllow) {
		t.Error("NodeClass preflight did not defer to exact per-node authorization")
	}
}

func TestAllowPseudoKindTuples_NodeClass_AllowedWithProviderSAR(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	// Bob has EC2 RBAC only. Root preflight without a known provider group
	// must still allow — the per-node Allow gate will then drop AKS/GCP
	// variants. This mirrors topology-strip semantics: a single provider
	// grant is sufficient for the kind-level gate.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)
	s.permCache.Set("bob", nil, perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "bob"})
	tuples, fallthroughAllow := pseudoKindTuplesForTest("nodeclass", "")
	if !s.allowPseudoKindTuples(r, tuples, fallthroughAllow) {
		t.Error("nodeclass root preflight denied user with EC2 get-SAR — single-provider RBAC must pass")
	}
}

// Same shape for NodePool, which has a SINGLE row in the table (karpenter.sh).
// Pin that the kind-only path works for single-entry pseudo-kinds too.
func TestAllowPseudoKindTuples_NodePool(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	denyPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	denyPerms.SetCanI("get", "karpenter.sh", "nodepools", "", false)
	s.permCache.Set("alice", nil, denyPerms)

	allowPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	allowPerms.SetCanI("get", "karpenter.sh", "nodepools", "", true)
	s.permCache.Set("bob", nil, allowPerms)

	tuples, fallthroughAllow := pseudoKindTuplesForTest("nodepool", "")
	if len(tuples) != 1 {
		t.Fatalf("expected 1 nodepool tuple, got %d", len(tuples))
	}
	rDeny := requestWithUser("GET", "/api/ai/neighborhood/nodepool/_/x", &auth.User{Username: "alice"})
	if s.allowPseudoKindTuples(rDeny, tuples, fallthroughAllow) {
		t.Error("nodepool root preflight allowed user without karpenter.sh/nodepools get-SAR")
	}
	rAllow := requestWithUser("GET", "/api/ai/neighborhood/nodepool/_/x", &auth.User{Username: "bob"})
	if !s.allowPseudoKindTuples(rAllow, tuples, fallthroughAllow) {
		t.Error("nodepool root preflight denied user WITH karpenter.sh/nodepools get-SAR")
	}
}

// Unqualified NodeClass roots defer provider authorization until the concrete
// topology node resolves its API group and resource.
func TestNeighborhood_NodeClassRootPreflightNotBadRequest(t *testing.T) {
	withWatchedNeighborhoodKinds(t)
	env := newAuthTestServer(t)

	// Unauthorized: missing root remains 404, not a namespace error.
	denyPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	denyPerms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	denyPerms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	denyPerms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)
	env.srv.permCache.Set("alice", nil, denyPerms)

	resp := env.authGet(t, "/api/ai/neighborhood/nodeclass/_/foo", "alice", "")
	resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		t.Error("unqualified nodeclass was misclassified as namespaced")
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unauthorized user got %d for missing nodeclass — want 404", resp.StatusCode)
	}

	// Authorized: EC2 get-SAR allowed → preflight passes; BFS finds no
	// NodeClass node in the seeded cache → 404, not 400.
	allowPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	allowPerms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	allowPerms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	allowPerms.SetCanI("get", "karpenter.k8s.gcp", "gcenodeclasses", "", false)
	env.srv.permCache.Set("bob", nil, allowPerms)

	resp2 := env.authGet(t, "/api/ai/neighborhood/nodeclass/_/foo", "bob", "")
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusBadRequest {
		t.Error("unqualified nodeclass was misclassified as namespaced")
	}
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("authorized user got %d for nonexistent nodeclass — want 404 (preflight passed, BFS empty)", resp2.StatusCode)
	}
}

// NodePool roots are cluster-scoped and never require a namespace.
func TestNeighborhood_NodePoolRootPreflightNotBadRequest(t *testing.T) {
	withWatchedNeighborhoodKinds(t)
	env := newAuthTestServer(t)

	denyPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	denyPerms.SetCanI("get", "karpenter.sh", "nodepools", "", false)
	env.srv.permCache.Set("alice", nil, denyPerms)
	resp := env.authGet(t, "/api/ai/neighborhood/nodepool/_/foo", "alice", "")
	resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		t.Errorf("nodepool root preflight returned 400 — pseudo-kind misclassified as namespaced")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unauthorized user got %d for nodepool — want 403", resp.StatusCode)
	}

	allowPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	allowPerms.SetCanI("get", "karpenter.sh", "nodepools", "", true)
	env.srv.permCache.Set("bob", nil, allowPerms)
	resp2 := env.authGet(t, "/api/ai/neighborhood/nodepool/_/foo", "bob", "")
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusBadRequest {
		t.Errorf("nodepool root preflight returned 400 even with karpenter.sh SAR")
	}
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("authorized user got %d for nonexistent nodepool — want 404", resp2.StatusCode)
	}
}

// TestNeighborhood_SecretRootIncluded pins the IncludeSecrets fix at the
// REST handler boundary. DefaultBuildOptions sets IncludeSecrets=false, so
// Secret nodes don't enter the topology and root lookup for kind=secret
// always returns "resource not found in topology" → 404, even for users
// authorized to read the Secret. The handler must override
// IncludeSecrets=true so authorized callers find the root, while the
// per-namespace `get secrets` Allow gate still 404s unauthorized callers
// via the empty-subgraph path.
//
// The seeded fake client (server_smoke_test.go TestMain) has a Secret
// "nginx-tls" in "default". This test depends on that seed.
func TestNeighborhood_SecretRootIncluded(t *testing.T) {
	withNeighborhoodDiscovery(t, neighborhoodTestDiscovery())
	env := newAuthTestServer(t)

	// Authorized user: namespace access to default + per-namespace
	// `get secrets` SAR. Both Allow checks (namespace + per-kind Secret)
	// must pass.
	env.srv.permCache.Set("bob", nil, &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	bobPerms := env.srv.permCache.Get("bob", nil)
	bobPerms.SetCanI("get", "", "secrets", "default", true)

	resp := env.authGet(t, "/api/ai/neighborhood/secret/default/nginx-tls", "bob", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("authorized user got %d for Secret root — IncludeSecrets override must surface Secret nodes in topology", resp.StatusCode)
	}

	// Unauthorized user: namespace access to default but NO per-namespace
	// `get secrets` SAR. Allow rejects the root → empty subgraph → 404.
	// Existence-hiding preserved: same 404 the IncludeSecrets=false path
	// produced, but now driven by RBAC instead of topology elision.
	env.srv.permCache.Set("alice", nil, &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	alicePerms := env.srv.permCache.Get("alice", nil)
	alicePerms.SetCanI("get", "", "secrets", "default", false)

	resp2 := env.authGet(t, "/api/ai/neighborhood/secret/default/nginx-tls", "alice", "")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("unauthorized user got %d for Secret root — Allow gate must produce 404 via empty subgraph (existence-hiding)", resp2.StatusCode)
	}
}

// A Role needs the caller's own permission whether or not discovery lists the
// RBAC group, since the typed cache reads it either way; and before discovery
// has served anything the neighborhood doesn't answer at all.
func TestNeighborhood_RoleRootNeedsItsOwnPermissionWithoutDiscovery(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "web-role", Namespace: "shop", UID: "role-uid"}},
	))
	env := newAuthTestServer(t)
	get := func(user string, grant bool) int {
		t.Helper()
		env.srv.permCache.Set(user, nil, &auth.UserPermissions{AllowedNamespaces: []string{"shop"}})
		env.srv.permCache.Get(user, nil).SetCanI("get", "rbac.authorization.k8s.io", "roles", "shop", grant)
		resp := env.authGet(t, "/api/ai/neighborhood/role/shop/web-role", user, "")
		resp.Body.Close()
		return resp.StatusCode
	}
	t.Run("rbac group missing from discovery", func(t *testing.T) {
		withNeighborhoodDiscovery(t, neighborhoodTestDiscovery())
		if got := get("role-granted", true); got != http.StatusOK {
			t.Fatalf("user who may get roles: %d, want 200; the fixture no longer reaches the Role", got)
		}
		if got := get("role-denied", false); got != http.StatusNotFound {
			t.Errorf("user who may not get roles: %d, want 404", got)
		}
	})
	t.Run("empty discovery", func(t *testing.T) {
		withNeighborhoodDiscovery(t, nil)
		for _, grant := range []bool{false, true} {
			if got := get(fmt.Sprintf("role-empty-%v", grant), grant); got != http.StatusServiceUnavailable {
				t.Errorf("get roles %v with nothing discovered: %d, want 503", grant, got)
			}
		}
	})
}

// A root Radar doesn't collect answers with the read's own code and status, so
// a caller can tell it from one that doesn't exist; a caller who may not read
// the root still gets 404.
func TestNeighborhood_UncollectedRootAnswersWithItsReadCode(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "shop"}},
	), map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true}, k8score.Namespaces: {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	withNeighborhoodDiscovery(t, neighborhoodTestDiscovery())
	env := newAuthTestServer(t)
	for _, grant := range []bool{true, false} {
		user := fmt.Sprintf("secret-reader-%v", grant)
		env.srv.permCache.Set(user, nil, &auth.UserPermissions{AllowedNamespaces: []string{"shop"}})
		env.srv.permCache.Get(user, nil).SetCanI("get", "", "secrets", "shop", grant)
		resp := env.authGet(t, "/api/ai/neighborhood/secret/shop/key", user, "")
		var body struct {
			ErrorCode readreason.Code `json:"error_code"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		switch {
		case grant && (resp.StatusCode != http.StatusServiceUnavailable || body.ErrorCode != readreason.RadarKindNotCollected):
			t.Errorf("may get secrets: %d %q, want 503 %s", resp.StatusCode, body.ErrorCode, readreason.RadarKindNotCollected)
		case !grant && (resp.StatusCode != http.StatusNotFound || body.ErrorCode != ""):
			t.Errorf("may not get secrets: %d %q, want 404 with no code", resp.StatusCode, body.ErrorCode)
		}
	}
}

// virtualServicesDeniedToRadar installs discovery serving VirtualServices and
// Services, and a dynamic cache whose list of VirtualServices the API server
// refuses, as when Radar's identity may not read the kind. watch says whether
// Radar has tried to collect it.
func virtualServicesDeniedToRadar(t *testing.T, watch bool) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
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
	if !watch {
		return
	}
	dc := k8s.GetDynamicResourceCache()
	_ = dc.EnsureWatching(gvr)
	for deadline := time.Now().Add(5 * time.Second); dc.Observation(gvr).State != k8score.DynamicObservationDenied; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the denial was never observed: %+v", dc.Observation(gvr))
		}
	}
}

// A root whose kind Radar may not read or doesn't collect says so with the
// read's code and status, not "not found", and is never answered with a
// same-named object of another kind.
func TestNeighborhood_RootKindDeniedToRadarSaysSo(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "bookinfo"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "reviews", Namespace: "bookinfo"}},
	))
	for _, tc := range []struct {
		watch bool
		want  readreason.Code
	}{{true, readreason.RadarForbidden}, {false, readreason.RadarKindNotCollected}} {
		t.Run(string(tc.want), func(t *testing.T) {
			virtualServicesDeniedToRadar(t, tc.watch)
			env := newAuthTestServer(t)
			env.srv.permCache.Set("istio-reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{"bookinfo"}})
			resp := env.authGet(t, "/api/ai/neighborhood/virtualservice/bookinfo/reviews?group=networking.istio.io&hops=2", "istio-reader", "")
			var body struct {
				Error     string          `json:"error"`
				ErrorCode readreason.Code `json:"error_code"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable || body.ErrorCode != tc.want || strings.Contains(body.Error, "not found") {
				t.Fatalf("got %d %+v, want 503 %s", resp.StatusCode, body, tc.want)
			}
		})
	}
}
