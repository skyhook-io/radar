package topology

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// v1 Claim -> bound XR -> composed MRs, via spec.resourceRef (singular) on the
// Claim and spec.resourceRefs (plural) on the XR.
func TestBuildCrossplaneV1ClaimChainEdges(t *testing.T) {
	claimGVR := schema.GroupVersionResource{Group: "demo.example.io", Version: "v1alpha1", Resource: "databaseclaims"}
	xrGVR := schema.GroupVersionResource{Group: "demo.example.io", Version: "v1alpha1", Resource: "xdatabases"}
	objGVR := schema.GroupVersionResource{Group: "kubernetes.crossplane.io", Version: "v1alpha2", Resource: "objects"}

	claim := karpenterTopologyObject("demo.example.io/v1alpha1", "DatabaseClaim", "example-database", "claim-uid", map[string]any{
		"spec": map[string]any{
			"compositionRef": map[string]any{"name": "xdatabases.demo.example.io"},
			"resourceRef": map[string]any{
				"apiVersion": "demo.example.io/v1alpha1", "kind": "XDatabase", "name": "example-database-xr",
			},
		},
	})
	claim.SetNamespace("demo-app")

	xr := karpenterTopologyObject("demo.example.io/v1alpha1", "XDatabase", "example-database-xr", "xr-uid", map[string]any{
		"spec": map[string]any{
			"resourceRefs": []any{
				map[string]any{"apiVersion": "kubernetes.crossplane.io/v1alpha2", "kind": "Object", "name": "example-database-configmap"},
				map[string]any{"apiVersion": "kubernetes.crossplane.io/v1alpha2", "kind": "Object", "name": "example-database-service"},
				map[string]any{"apiVersion": "kubernetes.crossplane.io/v1alpha2", "kind": "Object", "name": "example-database-connection"},
			},
		},
	})

	mr := func(name string) *unstructured.Unstructured {
		return karpenterTopologyObject("kubernetes.crossplane.io/v1alpha2", "Object", name, name+"-uid", map[string]any{
			"spec": map[string]any{"providerConfigRef": map[string]any{"name": "default"}},
		})
	}

	dynamic := &karpenterDynamicProvider{
		exact: map[string]schema.GroupVersionResource{},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			claimGVR: {claim},
			xrGVR:    {xr},
			objGVR:   {mr("example-database-configmap"), mr("example-database-service"), mr("example-database-connection")},
		},
		kinds: map[schema.GroupVersionResource]string{
			claimGVR: "DatabaseClaim", xrGVR: "XDatabase", objGVR: "Object",
		},
		watched:            []schema.GroupVersionResource{claimGVR, xrGVR, objGVR},
		listCalls:          make(map[schema.GroupVersionResource]int),
		listNamespaceCalls: make(map[schema.GroupVersionResource]int),
	}

	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	claimID := "databaseclaim/demo-app/example-database/demo.example.io"
	xrID := "xdatabase//example-database-xr/demo.example.io"
	composed := []string{"object//example-database-configmap/kubernetes.crossplane.io", "object//example-database-service/kubernetes.crossplane.io", "object//example-database-connection/kubernetes.crossplane.io"}

	for _, id := range append([]string{claimID, xrID}, composed...) {
		if findNode(topo, id) == nil {
			t.Fatalf("missing Crossplane node %q; nodes=%+v", id, topo.Nodes)
		}
	}
	if !hasKarpenterTopologyEdge(topo, claimID, xrID, EdgeManages) {
		t.Fatalf("missing claim -> XR manages edge; edges=%+v", topo.Edges)
	}
	for _, mrID := range composed {
		if !hasKarpenterTopologyEdge(topo, xrID, mrID, EdgeManages) {
			t.Fatalf("missing XR -> composed edge %s; edges=%+v", mrID, topo.Edges)
		}
	}
	// A claim has no composed refs of its own — no direct claim -> MR edge.
	for _, mrID := range composed {
		if hasKarpenterTopologyEdge(topo, claimID, mrID, EdgeManages) {
			t.Fatalf("unexpected claim -> composed edge %s", mrID)
		}
	}
}

// v2 namespaced XR -> composed namespaced MRs, via spec.crossplane.resourceRefs.
func TestBuildCrossplaneV2NamespacedXREdges(t *testing.T) {
	xrGVR := schema.GroupVersionResource{Group: "demo.example.io", Version: "v1alpha1", Resource: "appstacks"}
	objGVR := schema.GroupVersionResource{Group: "kubernetes.m.crossplane.io", Version: "v1alpha1", Resource: "objects"}

	xr := karpenterTopologyObject("demo.example.io/v1alpha1", "AppStack", "web-stack", "xr-uid", map[string]any{
		"spec": map[string]any{
			"crossplane": map[string]any{
				"resourceRefs": []any{
					map[string]any{"apiVersion": "kubernetes.m.crossplane.io/v1alpha1", "kind": "Object", "name": "web-stack-configmap"},
					map[string]any{"apiVersion": "kubernetes.m.crossplane.io/v1alpha1", "kind": "Object", "name": "web-stack-service"},
				},
			},
		},
	})
	xr.SetNamespace("v2-app")

	mr := func(name string) *unstructured.Unstructured {
		o := karpenterTopologyObject("kubernetes.m.crossplane.io/v1alpha1", "Object", name, name+"-uid", map[string]any{
			"spec": map[string]any{"providerConfigRef": map[string]any{"name": "default", "kind": "ProviderConfig"}},
		})
		o.SetNamespace("v2-app")
		return o
	}

	dynamic := &karpenterDynamicProvider{
		exact: map[string]schema.GroupVersionResource{},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			xrGVR:  {xr},
			objGVR: {mr("web-stack-configmap"), mr("web-stack-service")},
		},
		kinds: map[schema.GroupVersionResource]string{
			xrGVR: "AppStack", objGVR: "Object",
		},
		watched:            []schema.GroupVersionResource{xrGVR, objGVR},
		listCalls:          make(map[schema.GroupVersionResource]int),
		listNamespaceCalls: make(map[schema.GroupVersionResource]int),
	}

	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	xrID := "appstack/v2-app/web-stack/demo.example.io"
	composed := []string{"object/v2-app/web-stack-configmap/kubernetes.m.crossplane.io", "object/v2-app/web-stack-service/kubernetes.m.crossplane.io"}
	if findNode(topo, xrID) == nil {
		t.Fatalf("missing namespaced XR node %q; nodes=%+v", xrID, topo.Nodes)
	}
	for _, mrID := range composed {
		if findNode(topo, mrID) == nil {
			t.Fatalf("missing composed node %q; nodes=%+v", mrID, topo.Nodes)
		}
		if !hasKarpenterTopologyEdge(topo, xrID, mrID, EdgeManages) {
			t.Fatalf("missing v2 XR -> composed edge %s; edges=%+v", mrID, topo.Edges)
		}
	}
}

// Cluster-scoped Crossplane XR/MR nodes must be authorizable by their exact
// GVR so the topology RBAC strip can drop them for a user who can't list them.
// Without this the pass leaks cluster-scoped resource identities.
func TestCrossplaneClusterScopedNodesAreRBACStrippable(t *testing.T) {
	claimGVR := schema.GroupVersionResource{Group: "demo.example.io", Version: "v1alpha1", Resource: "databaseclaims"}
	xrGVR := schema.GroupVersionResource{Group: "demo.example.io", Version: "v1alpha1", Resource: "xdatabases"}
	objGVR := schema.GroupVersionResource{Group: "kubernetes.crossplane.io", Version: "v1alpha2", Resource: "objects"}

	claim := karpenterTopologyObject("demo.example.io/v1alpha1", "DatabaseClaim", "db", "c", map[string]any{
		"spec": map[string]any{
			"compositionRef": map[string]any{"name": "x"},
			"resourceRef":    map[string]any{"apiVersion": "demo.example.io/v1alpha1", "kind": "XDatabase", "name": "db-xr"},
		},
	})
	claim.SetNamespace("demo-app")
	xr := karpenterTopologyObject("demo.example.io/v1alpha1", "XDatabase", "db-xr", "x", map[string]any{
		"spec": map[string]any{"resourceRefs": []any{
			map[string]any{"apiVersion": "kubernetes.crossplane.io/v1alpha2", "kind": "Object", "name": "db-cm"},
		}},
	})
	mr := karpenterTopologyObject("kubernetes.crossplane.io/v1alpha2", "Object", "db-cm", "m", map[string]any{
		"spec": map[string]any{"providerConfigRef": map[string]any{"name": "default"}},
	})

	newProvider := func() *karpenterDynamicProvider {
		return &karpenterDynamicProvider{
			exact:              map[string]schema.GroupVersionResource{},
			resources:          map[schema.GroupVersionResource][]*unstructured.Unstructured{claimGVR: {claim}, xrGVR: {xr}, objGVR: {mr}},
			kinds:              map[schema.GroupVersionResource]string{claimGVR: "DatabaseClaim", xrGVR: "XDatabase", objGVR: "Object"},
			watched:            []schema.GroupVersionResource{claimGVR, xrGVR, objGVR},
			listCalls:          make(map[schema.GroupVersionResource]int),
			listNamespaceCalls: make(map[schema.GroupVersionResource]int),
		}
	}

	xrTuple := SARTuple{Group: "demo.example.io", Resource: "xdatabases"}
	objTuple := SARTuple{Group: "kubernetes.crossplane.io", Resource: "objects"}

	// the cluster-scoped nodes advertise their GVR tuples
	topo, _ := NewBuilder(&mockProvider{}).WithDynamic(newProvider()).Build(DefaultBuildOptions())
	got := map[SARTuple]bool{}
	for _, tp := range topo.ClusterScopedDynamicRBACTuples() {
		got[tp] = true
	}
	if !got[xrTuple] || !got[objTuple] {
		t.Fatalf("cluster-scoped tuples not advertised: %+v", got)
	}

	// deny everything: cluster-scoped XR + MR gone, namespaced Claim stays
	topo.StripClusterScopedDynamicExcept(map[SARTuple]bool{})
	if findNode(topo, "databaseclaim/demo-app/db/demo.example.io") == nil {
		t.Fatal("namespaced Claim node was wrongly stripped")
	}
	if findNode(topo, "xdatabase//db-xr/demo.example.io") != nil || findNode(topo, "object//db-cm/kubernetes.crossplane.io") != nil {
		t.Fatalf("unauthorized cluster-scoped node leaked through strip; nodes=%+v", topo.Nodes)
	}

	// allow only the XR's GVR: XR stays, MR still stripped
	topo2, _ := NewBuilder(&mockProvider{}).WithDynamic(newProvider()).Build(DefaultBuildOptions())
	topo2.StripClusterScopedDynamicExcept(map[SARTuple]bool{xrTuple: true})
	if findNode(topo2, "xdatabase//db-xr/demo.example.io") == nil {
		t.Fatal("authorized XR node was wrongly stripped")
	}
	if findNode(topo2, "object//db-cm/kubernetes.crossplane.io") != nil {
		t.Fatal("unauthorized MR node leaked (only XR was allowed)")
	}
}

// A cluster-scoped XR/MR (empty namespace) must survive a namespace filter, or
// the Claim->XR->composed chain silently breaks in a filtered topology view.
func TestBuildCrossplaneClusterScopedSurvivesNamespaceFilter(t *testing.T) {
	claimGVR := schema.GroupVersionResource{Group: "demo.example.io", Version: "v1alpha1", Resource: "databaseclaims"}
	xrGVR := schema.GroupVersionResource{Group: "demo.example.io", Version: "v1alpha1", Resource: "xdatabases"}
	objGVR := schema.GroupVersionResource{Group: "kubernetes.crossplane.io", Version: "v1alpha2", Resource: "objects"}

	claim := karpenterTopologyObject("demo.example.io/v1alpha1", "DatabaseClaim", "db", "claim-uid", map[string]any{
		"spec": map[string]any{
			"compositionRef": map[string]any{"name": "xdatabases.demo.example.io"},
			"resourceRef":    map[string]any{"apiVersion": "demo.example.io/v1alpha1", "kind": "XDatabase", "name": "db-xr"},
		},
	})
	claim.SetNamespace("demo-app")
	xr := karpenterTopologyObject("demo.example.io/v1alpha1", "XDatabase", "db-xr", "xr-uid", map[string]any{
		"spec": map[string]any{"resourceRefs": []any{
			map[string]any{"apiVersion": "kubernetes.crossplane.io/v1alpha2", "kind": "Object", "name": "db-cm"},
		}},
	})
	mr := karpenterTopologyObject("kubernetes.crossplane.io/v1alpha2", "Object", "db-cm", "cm-uid", map[string]any{
		"spec": map[string]any{"providerConfigRef": map[string]any{"name": "default"}},
	})

	dynamic := &karpenterDynamicProvider{
		exact: map[string]schema.GroupVersionResource{},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			claimGVR: {claim}, xrGVR: {xr}, objGVR: {mr},
		},
		kinds:              map[schema.GroupVersionResource]string{claimGVR: "DatabaseClaim", xrGVR: "XDatabase", objGVR: "Object"},
		watched:            []schema.GroupVersionResource{claimGVR, xrGVR, objGVR},
		listCalls:          make(map[schema.GroupVersionResource]int),
		listNamespaceCalls: make(map[schema.GroupVersionResource]int),
	}

	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"demo-app"} // filter active
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if !hasKarpenterTopologyEdge(topo, "databaseclaim/demo-app/db/demo.example.io", "xdatabase//db-xr/demo.example.io", EdgeManages) {
		t.Fatalf("cluster-scoped XR dropped under namespace filter; edges=%+v", topo.Edges)
	}
	if !hasKarpenterTopologyEdge(topo, "xdatabase//db-xr/demo.example.io", "object//db-cm/kubernetes.crossplane.io", EdgeManages) {
		t.Fatalf("cluster-scoped composed MR dropped under namespace filter; edges=%+v", topo.Edges)
	}
}

// Same-named composed resources in different namespaces must not collide: each
// namespaced XR must wire to the MR in ITS OWN namespace.
func TestBuildCrossplaneSameNameDifferentNamespaceNoCollision(t *testing.T) {
	xrGVR := schema.GroupVersionResource{Group: "demo.example.io", Version: "v1alpha1", Resource: "appstacks"}
	objGVR := schema.GroupVersionResource{Group: "kubernetes.m.crossplane.io", Version: "v1alpha1", Resource: "objects"}

	xrIn := func(ns string) *unstructured.Unstructured {
		o := karpenterTopologyObject("demo.example.io/v1alpha1", "AppStack", "stack", "xr-"+ns, map[string]any{
			"spec": map[string]any{"crossplane": map[string]any{"resourceRefs": []any{
				// same composed name "shared-config" in both namespaces, no namespace on the ref
				map[string]any{"apiVersion": "kubernetes.m.crossplane.io/v1alpha1", "kind": "Object", "name": "shared-config"},
			}}},
		})
		o.SetNamespace(ns)
		return o
	}
	mrIn := func(ns string) *unstructured.Unstructured {
		o := karpenterTopologyObject("kubernetes.m.crossplane.io/v1alpha1", "Object", "shared-config", "mr-"+ns, map[string]any{
			"spec": map[string]any{"providerConfigRef": map[string]any{"name": "default", "kind": "ProviderConfig"}},
		})
		o.SetNamespace(ns)
		return o
	}

	dynamic := &karpenterDynamicProvider{
		exact: map[string]schema.GroupVersionResource{},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			xrGVR:  {xrIn("team-a"), xrIn("team-b")},
			objGVR: {mrIn("team-a"), mrIn("team-b")},
		},
		kinds:              map[schema.GroupVersionResource]string{xrGVR: "AppStack", objGVR: "Object"},
		watched:            []schema.GroupVersionResource{xrGVR, objGVR},
		listCalls:          make(map[schema.GroupVersionResource]int),
		listNamespaceCalls: make(map[schema.GroupVersionResource]int),
	}

	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// each XR wires to the MR in its OWN namespace, not the other's
	if !hasKarpenterTopologyEdge(topo, "appstack/team-a/stack/demo.example.io", "object/team-a/shared-config/kubernetes.m.crossplane.io", EdgeManages) {
		t.Fatalf("team-a XR did not wire to its own composed MR; edges=%+v", topo.Edges)
	}
	if !hasKarpenterTopologyEdge(topo, "appstack/team-b/stack/demo.example.io", "object/team-b/shared-config/kubernetes.m.crossplane.io", EdgeManages) {
		t.Fatalf("team-b XR did not wire to its own composed MR; edges=%+v", topo.Edges)
	}
	// and NOT to the other namespace's MR
	if hasKarpenterTopologyEdge(topo, "appstack/team-a/stack/demo.example.io", "object/team-b/shared-config/kubernetes.m.crossplane.io", EdgeManages) {
		t.Fatalf("cross-namespace collision: team-a XR wired to team-b MR")
	}
	if hasKarpenterTopologyEdge(topo, "appstack/team-b/stack/demo.example.io", "object/team-a/shared-config/kubernetes.m.crossplane.io", EdgeManages) {
		t.Fatalf("cross-namespace collision: team-b XR wired to team-a MR")
	}
}

func TestBuildCrossplaneSameIdentityAcrossProviderGroups(t *testing.T) {
	awsGVR := schema.GroupVersionResource{Group: "s3.aws.upbound.io", Version: "v1beta1", Resource: "buckets"}
	gcpGVR := schema.GroupVersionResource{Group: "storage.gcp.upbound.io", Version: "v1beta1", Resource: "buckets"}
	bucket := func(apiVersion, uid string) *unstructured.Unstructured {
		o := karpenterTopologyObject(apiVersion, "Bucket", "artifacts", uid, map[string]any{
			"spec": map[string]any{"providerConfigRef": map[string]any{"name": "default"}},
		})
		o.SetNamespace("platform")
		return o
	}
	dynamic := &karpenterDynamicProvider{
		exact: map[string]schema.GroupVersionResource{},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			awsGVR: {bucket("s3.aws.upbound.io/v1beta1", "aws")},
			gcpGVR: {bucket("storage.gcp.upbound.io/v1beta1", "gcp")},
		},
		kinds:              map[schema.GroupVersionResource]string{awsGVR: "Bucket", gcpGVR: "Bucket"},
		watched:            []schema.GroupVersionResource{awsGVR, gcpGVR},
		listCalls:          make(map[schema.GroupVersionResource]int),
		listNamespaceCalls: make(map[schema.GroupVersionResource]int),
	}

	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	for _, id := range []string{
		"bucket/platform/artifacts/s3.aws.upbound.io",
		"bucket/platform/artifacts/storage.gcp.upbound.io",
	} {
		if findNode(topo, id) == nil {
			t.Fatalf("missing exact Crossplane node %q; nodes=%+v", id, topo.Nodes)
		}
	}
}
