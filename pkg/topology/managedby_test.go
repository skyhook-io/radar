package topology

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	k8score "github.com/skyhook-io/radar/pkg/k8score"
)

// stubDP is a minimal DynamicProvider for CRD-fallback regression tests in
// the topology package. Only GetGVR and Get are exercised by the metadata
// lookup path; the rest are stubs sufficient to satisfy the interface.
type stubDP struct {
	gvr map[string]schema.GroupVersionResource
	obj map[string]*unstructured.Unstructured // key = "ns/name"
}

func (s *stubDP) List(_ schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	return nil, nil
}
func (s *stubDP) ListNamespaces(_ schema.GroupVersionResource, _ []string) ([]*unstructured.Unstructured, error) {
	return nil, nil
}
func (s *stubDP) Get(_ schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if u, ok := s.obj[namespace+"/"+name]; ok {
		return u, nil
	}
	return nil, nil
}
func (s *stubDP) GetWatchedResources() []schema.GroupVersionResource { return nil }
func (s *stubDP) GetDiscoveryStatus() k8score.CRDDiscoveryStatus     { return k8score.CRDDiscoveryIdle }
func (s *stubDP) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	g, ok := s.gvr[kindOrName]
	return g, ok
}
func (s *stubDP) GetGVRWithGroup(kindOrName, _ string) (schema.GroupVersionResource, bool) {
	return s.GetGVR(kindOrName)
}
func (s *stubDP) GetKindForGVR(_ schema.GroupVersionResource) string { return "" }
func (s *stubDP) IsCRD(_ string) bool                                { return true }

func meta(labels, annos map[string]string) metav1.Object {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annos}}
}

// TestSynthesizeManagedBy_LabelsAndAnnotations covers precedence rules that
// mirror packages/k8s-ui/src/utils/gitops-owner.ts. The test order is the
// detection order — a stable contract because T14 will retire the frontend
// copy and assert equivalence.
func TestSynthesizeManagedBy_LabelsAndAnnotations(t *testing.T) {
	cases := []struct {
		name     string
		labels   map[string]string
		annos    map[string]string
		wantKind string
		wantNS   string
		wantName string
		wantGrp  string
	}{
		{
			name:     "Flux HelmRelease labels",
			labels:   map[string]string{fluxHelmNameLabel: "podinfo", fluxHelmNSLabel: "flux-system"},
			wantKind: "HelmRelease", wantNS: "flux-system", wantName: "podinfo", wantGrp: fluxHelmGroup,
		},
		{
			name: "Flux HelmRelease wins over Flux Kustomization",
			labels: map[string]string{
				fluxHelmNameLabel:      "podinfo",
				fluxHelmNSLabel:        "flux-system",
				fluxKustomizeNameLabel: "infra",
				fluxKustomizeNSLabel:   "flux-system",
			},
			wantKind: "HelmRelease", wantName: "podinfo", wantGrp: fluxHelmGroup, wantNS: "flux-system",
		},
		{
			name:     "Flux Kustomization labels",
			labels:   map[string]string{fluxKustomizeNameLabel: "infra", fluxKustomizeNSLabel: "flux-system"},
			wantKind: "Kustomization", wantNS: "flux-system", wantName: "infra", wantGrp: fluxKustomizeGroup,
		},
		{
			name: "Flux labels beat Argo tracking-id",
			labels: map[string]string{
				fluxHelmNameLabel: "podinfo",
				fluxHelmNSLabel:   "flux-system",
			},
			annos:    map[string]string{argoTrackingIDAnnotation: "argocd_other-app:apps/Deployment:default/web"},
			wantKind: "HelmRelease", wantName: "podinfo", wantGrp: fluxHelmGroup, wantNS: "flux-system",
		},
		{
			name:     "Argo tracking-id namespaced form",
			annos:    map[string]string{argoTrackingIDAnnotation: "argocd_my-app:apps/Deployment:default/web"},
			wantKind: "Application", wantNS: "argocd", wantName: "my-app", wantGrp: argoApplicationGroup,
		},
		{
			name:     "Argo tracking-id legacy single-name form yields empty namespace",
			annos:    map[string]string{argoTrackingIDAnnotation: "my-app:apps/Deployment:default/web"},
			wantKind: "Application", wantNS: "", wantName: "my-app", wantGrp: argoApplicationGroup,
		},
		{
			name:     "Argo instance label fallback",
			labels:   map[string]string{argoInstanceLabel: "guestbook"},
			wantKind: "Application", wantNS: "", wantName: "guestbook", wantGrp: argoApplicationGroup,
		},
		{
			name:     "Argo tracking-id beats instance label",
			labels:   map[string]string{argoInstanceLabel: "wrong"},
			annos:    map[string]string{argoTrackingIDAnnotation: "argocd_right:apps/Deployment:default/web"},
			wantKind: "Application", wantNS: "argocd", wantName: "right", wantGrp: argoApplicationGroup,
		},
		{
			name:     "Helm release annotation",
			annos:    map[string]string{helmReleaseNameAnno: "cert-manager", helmReleaseNSAnno: "cert-manager"},
			wantKind: "HelmRelease", wantNS: "cert-manager", wantName: "cert-manager", wantGrp: "",
		},
		{
			// app.kubernetes.io/instance is stamped by every Helm chart and was
			// the legacy false-positive trigger for "Managed by …" chips on
			// plain Helm installs. Frontend dropped it; we don't fall back on it.
			name:   "standard k8s instance label is NOT treated as Argo",
			labels: map[string]string{"app.kubernetes.io/instance": "guestbook-healthy"},
		},
		{
			name: "no signals returns no manager",
		},
		{
			name:   "Flux HelmRelease requires both name and namespace labels",
			labels: map[string]string{fluxHelmNameLabel: "podinfo"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SynthesizeManagedBy(meta(c.labels, c.annos), "Deployment", "default", "web", nil, nil, nil)
			if c.wantKind == "" {
				if got != nil {
					t.Fatalf("want nil ManagedBy, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want 1 manager ref, got %d (%+v)", len(got), got)
			}
			r := got[0]
			if r.Kind != c.wantKind || r.Namespace != c.wantNS || r.Name != c.wantName || r.Group != c.wantGrp {
				t.Errorf("want {Kind:%q Group:%q NS:%q Name:%q}, got %+v",
					c.wantKind, c.wantGrp, c.wantNS, c.wantName, r)
			}
		})
	}
}

// TestSynthesizeManagedBy_TopologyOwnerChain verifies the case-6 fallback:
// when no GitOps/Helm signals are on the resource itself, we walk the
// EdgeManages chain in topology to the topmost ancestor. Pod -> ReplicaSet ->
// Deployment yields the Deployment.
func TestSynthesizeManagedBy_TopologyOwnerChain(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "replicaset/demo/web-abc", Kind: KindReplicaSet, Name: "web-abc"},
			{ID: "pod/demo/web-abc-xyz", Kind: KindPod, Name: "web-abc-xyz"},
		},
		Edges: []Edge{
			{ID: "d-rs", Source: "deployment/demo/web", Target: "replicaset/demo/web-abc", Type: EdgeManages},
			{ID: "rs-p", Source: "replicaset/demo/web-abc", Target: "pod/demo/web-abc-xyz", Type: EdgeManages},
		},
	}

	got := SynthesizeManagedBy(nil, "Pod", "demo", "web-abc-xyz", topo, nil, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 manager ref via topology walk, got %d (%+v)", len(got), got)
	}
	if got[0].Kind != "Deployment" || got[0].Name != "web" || got[0].Namespace != "demo" {
		t.Errorf("want topmost owner Deployment/demo/web, got %+v", got[0])
	}
}

// TestSynthesizeManagedBy_TopologyOwnerChain_Indexed verifies the indexed walk
// produces the same ref as the O(E) fallback. High-fanout callers (T6/T89/T12)
// will always pass an index; this asserts they get identical results.
func TestSynthesizeManagedBy_TopologyOwnerChain_Indexed(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "replicaset/demo/web-abc", Kind: KindReplicaSet, Name: "web-abc"},
			{ID: "pod/demo/web-abc-xyz", Kind: KindPod, Name: "web-abc-xyz"},
		},
		Edges: []Edge{
			{ID: "d-rs", Source: "deployment/demo/web", Target: "replicaset/demo/web-abc", Type: EdgeManages},
			{ID: "rs-p", Source: "replicaset/demo/web-abc", Target: "pod/demo/web-abc-xyz", Type: EdgeManages},
		},
	}
	idx := IndexByResource(topo)

	got := SynthesizeManagedBy(nil, "Pod", "demo", "web-abc-xyz", topo, nil, idx)
	if len(got) != 1 {
		t.Fatalf("want 1 manager ref via indexed walk, got %d (%+v)", len(got), got)
	}
	if got[0].Kind != "Deployment" || got[0].Name != "web" || got[0].Namespace != "demo" {
		t.Errorf("want topmost owner Deployment/demo/web, got %+v", got[0])
	}
}

// TestSynthesizeManagedBy_LabelsBeatTopologyWalk verifies precedence: a Pod
// carrying an Argo tracking-id annotation surfaces the Application instead
// of the topmost K8s owner from the topology.
func TestSynthesizeManagedBy_LabelsBeatTopologyWalk(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "replicaset/demo/web-abc", Kind: KindReplicaSet, Name: "web-abc"},
			{ID: "pod/demo/web-abc-xyz", Kind: KindPod, Name: "web-abc-xyz"},
		},
		Edges: []Edge{
			{ID: "d-rs", Source: "deployment/demo/web", Target: "replicaset/demo/web-abc", Type: EdgeManages},
			{ID: "rs-p", Source: "replicaset/demo/web-abc", Target: "pod/demo/web-abc-xyz", Type: EdgeManages},
		},
	}
	pod := meta(nil, map[string]string{argoTrackingIDAnnotation: "argocd_guestbook:apps/Deployment:demo/web"})

	got := SynthesizeManagedBy(pod, "Pod", "demo", "web-abc-xyz", topo, nil, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 manager ref, got %d (%+v)", len(got), got)
	}
	if got[0].Kind != "Application" || got[0].Name != "guestbook" || got[0].Namespace != "argocd" {
		t.Errorf("want Argo Application/argocd/guestbook, got %+v", got[0])
	}
}

// TestSynthesizeManagedBy_CycleSafe pins behavior on a self-referential
// EdgeManages chain. The walk must terminate AND must not return the
// starting node itself (which would produce a self-referential ManagedBy).
// Such a cycle is impossible from real K8s ownerReferences (single
// controller, parent set before child) but a corrupted topology should
// degrade gracefully — returning the first found owner, not self.
func TestSynthesizeManagedBy_CycleSafe(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/a", Kind: KindDeployment, Name: "a"},
			{ID: "deployment/demo/b", Kind: KindDeployment, Name: "b"},
		},
		Edges: []Edge{
			{ID: "a-b", Source: "deployment/demo/a", Target: "deployment/demo/b", Type: EdgeManages},
			{ID: "b-a", Source: "deployment/demo/b", Target: "deployment/demo/a", Type: EdgeManages},
		},
	}
	got := SynthesizeManagedBy(nil, "Deployment", "demo", "a", topo, nil, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 manager ref under cycle, got %d (%+v)", len(got), got)
	}
	if got[0].Name == "a" {
		t.Errorf("walkTopmostOwner returned the starting node as its own owner: %+v", got[0])
	}
	if got[0].Name != "b" {
		t.Errorf("want first found owner b, got %+v", got[0])
	}
}

// TestGetRelationships_BackCompat_PDBManagedByPreserved pins the regression
// Bugbot flagged on the original T2 commit: kinds with ResourceProvider
// methods but absent from the typed switch (PDB, NetworkPolicy, HPA, PVC, PV,
// Node) dropped the GitOps chip even though the old client-side
// detectGitOpsOwner worked on any kind. Production callers now pass obj
// directly via GetRelationshipsWithObject so this only matters for the
// back-compat path — but the typed switch should still cover them so the
// back-compat path matches old-UI parity. PDB is the canonical case since it
// connects via EdgeProtects (not EdgeManages), so the topology-walk fallback
// also can't recover the chip.
func TestGetRelationships_BackCompat_PDBManagedByPreserved(t *testing.T) {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod",
			Name:      "api-pdb",
			Annotations: map[string]string{
				argoTrackingIDAnnotation: "argocd_storefront:policy/PodDisruptionBudget:prod/api-pdb",
			},
		},
	}
	provider := &stubProvider{pdbs: []*policyv1.PodDisruptionBudget{pdb}}
	topo := &Topology{Nodes: []Node{{ID: "poddisruptionbudget/prod/api-pdb", Kind: KindPDB, Name: "api-pdb"}}}

	// Long-form and short-form aliases must both surface the chip; production
	// callers and back-compat callers can pass either, matching the other
	// switches in this file (storage chain) and server.go's kind resolution.
	for _, kind := range []string{"PodDisruptionBudget", "poddisruptionbudgets", "pdb", "pdbs"} {
		t.Run(kind, func(t *testing.T) {
			rel := GetRelationships(kind, "prod", "api-pdb", topo, provider, nil)
			if rel == nil || len(rel.ManagedBy) != 1 {
				t.Fatalf("want 1 ManagedBy ref for PDB via kind=%q, got %+v", kind, rel)
			}
			got := rel.ManagedBy[0]
			if got.Kind != "Application" || got.Namespace != "argocd" || got.Name != "storefront" {
				t.Errorf("want Argo Application/argocd/storefront via kind=%q, got %+v", kind, got)
			}
		})
	}
}

// TestGetRelationshipsWithObject_KindCollision_DisambiguatesByObject pins the
// reviewer's blocker on PR #720: a CRD whose plural collides with a core
// resource (Knative serving.knative.dev/Service vs core/v1 Service) MUST
// surface ManagedBy from the CRD's annotations, not from a co-named core
// Service that happens to share the namespace/name. The old kind/name-based
// lookup picked the core Service (no annotation) and silently dropped the
// chip; the new caller-passes-obj path uses the authoritative resource.
func TestGetRelationshipsWithObject_KindCollision_DisambiguatesByObject(t *testing.T) {
	knativeGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	knativeSvc := &unstructured.Unstructured{}
	knativeSvc.SetGroupVersionKind(schema.GroupVersionKind{Group: "serving.knative.dev", Version: "v1", Kind: "Service"})
	knativeSvc.SetNamespace("prod")
	knativeSvc.SetName("api")
	knativeSvc.SetAnnotations(map[string]string{
		argoTrackingIDAnnotation: "argocd_storefront:serving.knative.dev/Service:prod/api",
	})

	// Core Service shares ns/name and has NO managed-by signal. With the
	// old kind/name-only lookup path this was what lookupTypedMetadata
	// returned first, masking the Knative annotation.
	coreSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}}
	provider := &stubProvider{services: []*corev1.Service{coreSvc}}
	dp := &stubDP{
		gvr: map[string]schema.GroupVersionResource{
			"service":  knativeGVR,
			"services": knativeGVR,
		},
		obj: map[string]*unstructured.Unstructured{"prod/api": knativeSvc},
	}
	topo := &Topology{Nodes: []Node{{ID: "service/prod/api", Kind: KindService, Name: "api"}}}

	// New canonical path: caller passes the authoritative CRD object.
	rel := GetRelationshipsWithObject("Service", "prod", "api", knativeSvc, topo, provider, dp, nil)
	if rel == nil || len(rel.ManagedBy) != 1 {
		t.Fatalf("want 1 ManagedBy ref via caller-passed CRD obj, got %+v", rel)
	}
	got := rel.ManagedBy[0]
	if got.Kind != "Application" || got.Namespace != "argocd" || got.Name != "storefront" {
		t.Errorf("want Argo Application/argocd/storefront from CRD annotation, got %+v", got)
	}

	// Back-compat path (obj=nil): the fallback hits the core Service first
	// via lookupTypedMetadata and produces no ManagedBy. This pins the
	// collision behavior — callers that want correctness MUST migrate to
	// GetRelationshipsWithObject. If this assertion ever flips, the
	// fallback path has been made group-aware and the migration note in
	// the back-compat doc should be revisited.
	relFallback := GetRelationshipsWithIndex("Service", "prod", "api", topo, provider, dp, nil)
	if relFallback != nil && len(relFallback.ManagedBy) > 0 {
		t.Errorf("back-compat path unexpectedly produced ManagedBy from CRD annotation: %+v — fallback should still be group-blind; if this now works, update the doc on GetRelationships", relFallback.ManagedBy)
	}
}

// TestGetRelationships_CRD_ManagedByPreserved is the regression for the
// silent-disappear bug the reviewer flagged on #720: a CRD resource (e.g.
// cert-manager Certificate) with an ArgoCD tracking-id annotation must still
// surface ManagedBy. Before the lookupObjectMetadata split, CRDs fell off the
// typed switch and the chip disappeared without an "omitted" trace.
func TestGetRelationships_CRD_ManagedByPreserved(t *testing.T) {
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	cert.SetNamespace("prod")
	cert.SetName("api-tls")
	cert.SetAnnotations(map[string]string{
		argoTrackingIDAnnotation: "argocd_storefront:cert-manager.io/Certificate:prod/api-tls",
	})

	dp := &stubDP{
		gvr: map[string]schema.GroupVersionResource{
			"certificate":  certGVR,
			"certificates": certGVR,
		},
		obj: map[string]*unstructured.Unstructured{
			"prod/api-tls": cert,
		},
	}

	// Topology with the Certificate node only (no owner chain — annotation alone drives the chip).
	topo := &Topology{
		Nodes: []Node{{ID: "certificate/prod/api-tls", Kind: KindCertificate, Name: "api-tls"}},
	}

	rel := GetRelationshipsWithIndex("Certificate", "prod", "api-tls", topo, nil, dp, nil)
	if rel == nil || len(rel.ManagedBy) != 1 {
		t.Fatalf("want 1 ManagedBy ref for CRD with Argo tracking-id, got %+v", rel)
	}
	got := rel.ManagedBy[0]
	if got.Kind != "Application" || got.Namespace != "argocd" || got.Name != "storefront" {
		t.Errorf("want Argo Application/argocd/storefront from CRD annotation, got %+v", got)
	}
}

func TestParseArgoTrackingID(t *testing.T) {
	cases := []struct {
		in       string
		ns, name string
		ok       bool
	}{
		{"argocd_my-app:apps/Deployment:default/web", "argocd", "my-app", true},
		{"my-app:apps/Deployment:default/web", "", "my-app", true},
		{"just-garbage-no-colon", "", "", false},
		{"my-ns_:apps/Deployment:default/web", "", "", false},
		{":apps/Deployment:default/web", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			ns, name, ok := parseArgoTrackingID(c.in)
			if ok != c.ok || ns != c.ns || name != c.name {
				t.Errorf("parseArgoTrackingID(%q) = (%q,%q,%v); want (%q,%q,%v)",
					c.in, ns, name, ok, c.ns, c.name, c.ok)
			}
		})
	}
}
