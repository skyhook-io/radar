package resourcecontext

import (
	"context"
	"fmt"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"slices"
	"testing"
)

func TestBuild_StorageDependencyChain(t *testing.T) {
	class := "fast"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "prod"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "disk", StorageClassName: &class}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class, ClaimRef: &corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data"}}}
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: class}}
	opts := Options{Tier: TierBasic, Topology: &topology.Topology{}, Provider: mockResourceProvider{pvcs: []*corev1.PersistentVolumeClaim{pvc}, pvs: []*corev1.PersistentVolume{pv}}}
	rc := Build(context.Background(), pvc, opts)
	if len(rc.Dependencies) != 2 || rc.Dependencies[0].Kind != "StorageClass" || rc.Dependencies[0].Group != "storage.k8s.io" || rc.Dependencies[1].Kind != "PersistentVolume" {
		t.Fatalf("claim context: %+v", rc.Dependencies)
	}
	rc = Build(context.Background(), pv, opts)
	if len(rc.Dependencies) != 1 || rc.Dependencies[0].Kind != "StorageClass" || len(rc.Dependents) != 1 || rc.Dependents[0].Name != "data" {
		t.Fatalf("volume context: %+v", rc)
	}
	rel := topology.GetRelationshipsWithObject("StorageClass", "", "fast", sc, opts.Topology, opts.Provider, nil, nil)
	if rel == nil || len(rel.Dependents) != 1 || rel.Dependents[0].Kind != "PersistentVolumeClaim" || len(rel.Children) != 1 || rel.Children[0].Kind != "PersistentVolume" {
		t.Fatalf("class claims must be dependents, not owned children: %+v", rel)
	}
	rc = Build(context.Background(), sc, opts)
	if len(rc.Dependents) != 2 {
		t.Fatalf("class context: %+v", rc.Dependents)
	}
	pvc.Spec.VolumeName = ""
	rc = Build(context.Background(), pvc, opts)
	if len(rc.Dependencies) != 1 || rc.Dependencies[0].Kind != "StorageClass" {
		t.Fatalf("pending claim class: %+v", rc.Dependencies)
	}
	opts.AccessChecker = denyChecker{group: "storage.k8s.io", kind: "StorageClass"}
	rc = Build(context.Background(), pvc, opts)
	if len(rc.Dependencies) != 0 || !hasOmitted(rc.Omitted, "dependencies") {
		t.Fatalf("class permission gate: %+v", rc)
	}
	for _, kind := range []string{"PersistentVolumeClaim", "PersistentVolume", "StorageClass", "Node"} {
		custom := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "custom.example/v1", "kind": kind, "metadata": map[string]interface{}{"name": map[string]string{"PersistentVolumeClaim": "data", "PersistentVolume": "disk", "StorageClass": "fast", "Node": "worker"}[kind], "namespace": "prod"}}}
		got := Build(context.Background(), custom, opts)
		if len(got.Dependencies) != 0 || len(got.Dependents) != 0 {
			t.Fatalf("custom %s got core storage facts: %+v", kind, got)
		}
	}
}
func TestBuild_StorageContextBoundedAndExact(t *testing.T) {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}}
	refs := make([]topology.ResourceRef, 0, 30)
	for i := 0; i < 25; i++ {
		refs = append(refs, topology.ResourceRef{Kind: "PersistentVolumeClaim", Namespace: "visible", Name: fmt.Sprintf("claim-%02d", i)})
	}
	refs = append(refs, refs[0], topology.ResourceRef{Kind: "PersistentVolumeClaim", Namespace: "hidden", Name: "hidden"}, topology.ResourceRef{Kind: "PersistentVolumeClaim", Group: "custom.example", Namespace: "visible", Name: "fake"}, topology.ResourceRef{Kind: "Pod", Namespace: "visible", Name: "owned"})
	rc := Build(context.Background(), sc, Options{Tier: TierBasic, Relationships: &topology.Relationships{Dependents: refs[:27], Children: refs[27:]}, AccessChecker: denyChecker{kind: "PersistentVolumeClaim", namespace: "hidden"}})
	if len(rc.Dependents) != maxReferencedByItems || rc.Dependents[0].Name != "claim-00" || !hasOmitted(rc.Omitted, "dependents") {
		t.Fatalf("bounded references: %+v", rc)
	}
	if len(refs) != 29 || refs[0].Name != "claim-00" {
		t.Fatal("input references mutated")
	}
	for _, ref := range rc.Dependents {
		if ref.Group != "" || ref.Kind != "PersistentVolumeClaim" || ref.Namespace != "visible" {
			t.Fatalf("incorrect storage target: %+v", ref)
		}
	}
}

func TestBuild_StorageContextCapKeepsBothResourceKinds(t *testing.T) {
	class := "fast"
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: class}}
	provider := mockResourceProvider{pvs: []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class}}}}
	for i := 0; i < 1000; i++ {
		provider.pvcs = append(provider.pvcs, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("claim-%04d", i), Namespace: "prod"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class}})
	}
	opts := Options{Tier: TierBasic, Topology: &topology.Topology{}, Provider: provider}
	rc := Build(context.Background(), sc, opts)
	if len(rc.Dependents) != maxReferencedByItems || rc.Dependents[1].Kind != "PersistentVolume" || rc.Dependents[1].Name != "disk" || !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedBudgetExceeded}) {
		t.Fatalf("volume hidden by claim population: %+v", rc)
	}
	opts.AccessChecker = denyChecker{kind: "PersistentVolume"}
	rc = Build(context.Background(), sc, opts)
	if len(rc.Dependents) != maxReferencedByItems || rc.Dependents[19].Name != "claim-0019" {
		t.Fatalf("denied volume changed visible cap: %+v", rc.Dependents)
	}
}

func TestBuild_StorageAnnotationFollowsKubernetesClassReader(t *testing.T) {
	declared := "spec-class"
	annotations := map[string]string{corev1.BetaStorageClassAnnotation: "effective-class"}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "prod", Annotations: annotations}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &declared}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "volume", Annotations: annotations}, Spec: corev1.PersistentVolumeSpec{StorageClassName: declared}}
	opts := Options{Tier: TierBasic, Topology: &topology.Topology{}, Provider: mockResourceProvider{pvcs: []*corev1.PersistentVolumeClaim{pvc}, pvs: []*corev1.PersistentVolume{pv}}}
	claim := Build(context.Background(), pvc, opts)
	if len(claim.Dependencies) != 1 || claim.Dependencies[0].Name != "effective-class" || claim.PVCSummary.StorageClassName != "effective-class" {
		t.Fatalf("annotation class precedence lost: %+v", claim)
	}
	volume := Build(context.Background(), pv, opts)
	if len(volume.Dependencies) != 1 || volume.Dependencies[0].Name != "effective-class" {
		t.Fatalf("volume annotation class precedence lost: %+v", volume)
	}
	class := Build(context.Background(), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "effective-class"}}, opts)
	if len(class.Dependents) != 2 {
		t.Fatalf("annotation-only class membership lost: %+v", class)
	}
}

type countingRefChecker struct {
	calls           int
	deniedKind      string
	deniedNamespace string
}

func (c *countingRefChecker) CanRead(_ context.Context, _ string, kind, namespace string) bool {
	c.calls++
	return (c.deniedKind == "" || kind != c.deniedKind) && (c.deniedNamespace == "" || namespace != c.deniedNamespace)
}

func TestBuild_GenericDependencyBudgetStopsAuthorizationAfterVisibleOverflow(t *testing.T) {
	refs := make([]topology.ResourceRef, 0, 1000)
	for i := 0; i < 1000; i++ {
		refs = append(refs, topology.ResourceRef{Kind: "Service", Namespace: fmt.Sprintf("namespace-%04d", i), Name: "backend"})
	}
	checker := &countingRefChecker{}
	rc := Build(context.Background(), &corev1.Service{}, Options{Relationships: &topology.Relationships{Dependencies: refs}, AccessChecker: checker})
	if len(rc.Dependencies) != maxReferencedByItems || checker.calls != maxReferencedByItems+1 || !slices.Contains(rc.Omitted, OmittedField{Field: "dependencies", Reason: OmittedBudgetExceeded}) {
		t.Fatalf("generic budget checks were not bounded: refs=%d checks=%d omitted=%+v", len(rc.Dependencies), checker.calls, rc.Omitted)
	}
}

func TestBuild_StorageAuthorizationBudgetKeepsReadableVolume(t *testing.T) {
	refs := make([]topology.ResourceRef, 0, 1000)
	for i := 0; i < 1000; i++ {
		refs = append(refs, topology.ResourceRef{Kind: "PersistentVolumeClaim", Namespace: fmt.Sprintf("namespace-%04d", i), Name: "claim"})
	}
	checker := &countingRefChecker{deniedKind: "PersistentVolumeClaim"}
	rc := Build(context.Background(), &storagev1.StorageClass{}, Options{Relationships: &topology.Relationships{
		Dependents: refs, Children: []topology.ResourceRef{{Kind: "PersistentVolume", Name: "disk"}},
	}, AccessChecker: checker})
	if checker.calls != maxReferenceAccessChecks || len(rc.Dependents) != 1 || rc.Dependents[0].Name != "disk" {
		t.Fatalf("denied namespaces exhausted readable evidence: checks=%d refs=%+v", checker.calls, rc.Dependents)
	}
	for _, reason := range []OmittedReason{OmittedBudgetExceeded, OmittedRBACDenied} {
		if !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: reason}) {
			t.Fatalf("partial inventory lost %s: %+v", reason, rc.Omitted)
		}
	}
}

func TestBuild_ReferenceAccessBudgetCountsDistinctScopes(t *testing.T) {
	refs := make([]topology.ResourceRef, 0, 1001)
	for i := 0; i < 1000; i++ {
		refs = append(refs, topology.ResourceRef{Kind: "Service", Namespace: "denied", Name: fmt.Sprintf("service-%04d", i)})
	}
	refs = append(refs, topology.ResourceRef{Kind: "Service", Namespace: "visible", Name: "backend"})
	checker := &countingRefChecker{deniedNamespace: "denied"}
	rc := Build(context.Background(), &corev1.Service{}, Options{Relationships: &topology.Relationships{Dependencies: refs}, AccessChecker: checker})
	if checker.calls != 2 || len(rc.Dependencies) != 1 || rc.Dependencies[0].Name != "backend" || slices.Contains(rc.Omitted, OmittedField{Field: "dependencies", Reason: OmittedBudgetExceeded}) {
		t.Fatalf("repeated denied scope consumed authorization budget: checks=%d refs=%+v omitted=%+v", checker.calls, rc.Dependencies, rc.Omitted)
	}
}
