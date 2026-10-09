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
	rc := Build(context.Background(), sc, Options{Tier: TierBasic, Relationships: &topology.Relationships{Children: refs}, AccessChecker: denyChecker{kind: "PersistentVolumeClaim", namespace: "hidden"}})
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
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}}
	var refs []topology.ResourceRef
	for i := 0; i < 25; i++ {
		refs = append(refs, topology.ResourceRef{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: fmt.Sprintf("claim-%02d", i)})
	}
	refs = append(refs, topology.ResourceRef{Kind: "PersistentVolume", Name: "disk"})
	opts := Options{Tier: TierBasic, Relationships: &topology.Relationships{Children: refs}}
	rc := Build(context.Background(), sc, opts)
	if len(rc.Dependents) != maxReferencedByItems || rc.Dependents[1].Kind != "PersistentVolume" || rc.Dependents[1].Name != "disk" || !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedBudgetExceeded}) {
		t.Fatalf("volume hidden by claim population: %+v", rc)
	}
	// Denied volumes neither consume the cap nor alter the visible claim set.
	opts.AccessChecker = denyChecker{kind: "PersistentVolume"}
	rc = Build(context.Background(), sc, opts)
	if len(rc.Dependents) != maxReferencedByItems || rc.Dependents[19].Name != "claim-19" {
		t.Fatalf("denied volume changed visible cap: %+v", rc.Dependents)
	}
}
