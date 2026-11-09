package resourcecontext

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuild_StorageDependencyChain(t *testing.T) {
	class := "fast"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "prod"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "disk", StorageClassName: &class}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class, ClaimRef: &corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data"}}}
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: class}}
	opts := Options{Tier: TierBasic, Topology: &topology.Topology{}, Provider: mockResourceProvider{pvcs: []*corev1.PersistentVolumeClaim{pvc}, pvs: []*corev1.PersistentVolume{pv}}}

	rc := Build(context.Background(), pvc, opts)
	if len(rc.Dependencies) != 2 || !slices.Contains(rc.Dependencies, ContextRef{Kind: "PersistentVolume", Name: "disk"}) || !slices.Contains(rc.Dependencies, ContextRef{Kind: "StorageClass", Group: "storage.k8s.io", Name: "fast"}) {
		t.Fatalf("claim context: %+v", rc.Dependencies)
	}
	rc = Build(context.Background(), pv, opts)
	if len(rc.Dependencies) != 1 || rc.Dependencies[0].Kind != "StorageClass" || len(rc.Dependents) != 1 || rc.Dependents[0].Kind != "PersistentVolumeClaim" || rc.Dependents[0].Name != "data" {
		t.Fatalf("volume context: %+v", rc)
	}
	rc = Build(context.Background(), sc, opts)
	if len(rc.Dependents) != 2 {
		t.Fatalf("class context: %+v", rc.Dependents)
	}

	pvc.Spec.VolumeName = ""
	rc = Build(context.Background(), pvc, opts)
	if len(rc.Dependencies) != 1 || rc.Dependencies[0].Kind != "StorageClass" {
		t.Fatalf("a Pending claim must still lead to its class: %+v", rc.Dependencies)
	}

	opts.AccessChecker = denyChecker{group: "storage.k8s.io", kind: "StorageClass"}
	rc = Build(context.Background(), pvc, opts)
	if len(rc.Dependencies) != 0 || !hasOmitted(rc.Omitted, "dependencies") {
		t.Fatalf("class permission gate: %+v", rc)
	}

	for _, kind := range []string{"PersistentVolumeClaim", "PersistentVolume", "StorageClass"} {
		custom := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "custom.example/v1", "kind": kind, "metadata": map[string]any{"name": map[string]string{"PersistentVolumeClaim": "data", "PersistentVolume": "disk", "StorageClass": "fast"}[kind], "namespace": "prod"}}}
		got := Build(context.Background(), custom, opts)
		if len(got.Dependencies) != 0 || len(got.Dependents) != 0 {
			t.Fatalf("custom %s got core storage facts: %+v", kind, got)
		}
	}
}

func TestBuild_StorageClassClaimsAreBoundedAndKeepVolumes(t *testing.T) {
	class := "fast"
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: class}}
	provider := mockResourceProvider{pvs: []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class}}}}
	for i := 0; i < 1000; i++ {
		provider.pvcs = append(provider.pvcs, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("claim-%04d", i), Namespace: "prod"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class}})
	}
	opts := Options{Tier: TierBasic, Topology: &topology.Topology{}, Provider: provider}
	rc := Build(context.Background(), sc, opts)
	if len(rc.Dependents) != maxReferencedByItems || !slices.ContainsFunc(rc.Dependents, func(r ContextRef) bool { return r.Kind == "PersistentVolume" && r.Name == "disk" }) || !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedBudgetExceeded}) {
		t.Fatalf("claims crowded out the volume or the cap went unreported: %+v %+v", rc.Dependents, rc.Omitted)
	}
	opts.AccessChecker = denyChecker{kind: "PersistentVolume"}
	rc = Build(context.Background(), sc, opts)
	if len(rc.Dependents) != maxReferencedByItems || slices.ContainsFunc(rc.Dependents, func(r ContextRef) bool { return r.Kind == "PersistentVolume" }) {
		t.Fatalf("denied volume leaked or shrank the visible cap: %+v", rc.Dependents)
	}
}

type countingRefChecker struct {
	calls      int
	deniedKind string
}

func (c *countingRefChecker) CanRead(_ context.Context, _ string, kind, _ string) bool {
	c.calls++
	return kind != c.deniedKind
}

func TestBuild_StorageClassAuthorizationBudgetKeepsReadableVolume(t *testing.T) {
	refs := make([]topology.ResourceRef, 0, 1001)
	for i := 0; i < 1000; i++ {
		refs = append(refs, topology.ResourceRef{Kind: "PersistentVolumeClaim", Namespace: fmt.Sprintf("namespace-%04d", i), Name: "claim"})
	}
	refs = append(refs, topology.ResourceRef{Kind: "PersistentVolume", Name: "disk"})
	checker := &countingRefChecker{deniedKind: "PersistentVolumeClaim"}
	rc := Build(context.Background(), &storagev1.StorageClass{}, Options{Relationships: &topology.Relationships{Consumers: refs}, AccessChecker: checker})
	if checker.calls != maxReferenceAccessChecks || len(rc.Dependents) != 1 || rc.Dependents[0].Name != "disk" {
		t.Fatalf("denied namespaces exhausted readable evidence: checks=%d refs=%+v", checker.calls, rc.Dependents)
	}
	for _, reason := range []OmittedReason{OmittedBudgetExceeded, OmittedRBACDenied} {
		if !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: reason}) {
			t.Fatalf("partial answer lost %s: %+v", reason, rc.Omitted)
		}
	}
}

// assessingStorageProvider reports per-kind readiness for a StorageClass's
// volume and claim inventories, and can finish syncing claims mid-read.
type assessingStorageProvider struct {
	mockResourceProvider
	claims, volumes  k8score.KindReadAssessment
	syncOnClaimsRead bool
}

func (p *assessingStorageProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	if p.syncOnClaimsRead {
		p.claims = k8score.KindReadAssessment{Readiness: k8score.KindReady, AllNamespaces: true, Complete: true}
	}
	return p.pvcs, nil
}

func (p *assessingStorageProvider) AssessKindRead(resource string, _ []string) k8score.KindReadAssessment {
	if resource == string(k8score.PersistentVolumeClaims) {
		return p.claims
	}
	return p.volumes
}

func TestBuild_StorageClassReportsIncompleteInventories(t *testing.T) {
	complete := k8score.KindReadAssessment{Readiness: k8score.KindReady, AllNamespaces: true, Complete: true}
	loading := k8score.KindReadAssessment{Readiness: k8score.KindPending, AllNamespaces: true}
	failed := k8score.KindReadAssessment{Readiness: k8score.KindFailed, AllNamespaces: true}
	namespaced := k8score.KindReadAssessment{Readiness: k8score.KindReady, Read: []string{"team"}}
	class := "fast"
	volume := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class}}
	for _, tc := range []struct {
		name            string
		claims, volumes k8score.KindReadAssessment
		syncDuringRead  bool
		want            OmittedReason
	}{
		{"complete", complete, complete, false, ""},
		{"claims loading", loading, complete, false, OmittedCacheCold},
		{"claims synced during the read", loading, complete, true, OmittedCacheCold},
		{"claims failed", failed, complete, false, OmittedUnavailable},
		{"claims collected in some namespaces", namespaced, complete, false, OmittedUnavailable},
		{"volumes loading", complete, loading, false, OmittedCacheCold},
		{"unavailable outranks loading", failed, loading, false, OmittedUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &assessingStorageProvider{mockResourceProvider: mockResourceProvider{pvs: []*corev1.PersistentVolume{volume}}, claims: tc.claims, volumes: tc.volumes, syncOnClaimsRead: tc.syncDuringRead}
			rc := Build(context.Background(), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: class}}, Options{Tier: TierBasic, Topology: &topology.Topology{}, Provider: provider})
			if !slices.Contains(rc.Dependents, ContextRef{Kind: "PersistentVolume", Name: "disk"}) {
				t.Fatalf("available volume lost: %+v", rc.Dependents)
			}
			gaps := slices.DeleteFunc(slices.Clone(rc.Omitted), func(f OmittedField) bool { return f.Field != "dependents" })
			if tc.want == "" && len(gaps) != 0 || tc.want != "" && !slices.Contains(gaps, OmittedField{Field: "dependents", Reason: tc.want}) {
				t.Fatalf("dependents omissions = %+v, want %q", gaps, tc.want)
			}
		})
	}
}

func TestBuild_EmptyStorageClassStillReportsIncompleteInventory(t *testing.T) {
	provider := &assessingStorageProvider{
		claims:  k8score.KindReadAssessment{Readiness: k8score.KindPending, AllNamespaces: true},
		volumes: k8score.KindReadAssessment{Readiness: k8score.KindPending, AllNamespaces: true},
	}
	rc := Build(context.Background(), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}}, Options{Tier: TierBasic, Topology: &topology.Topology{}, Provider: provider})
	if len(rc.Dependents) != 0 || !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedCacheCold}) {
		t.Fatalf("an empty, still-loading class read as unused: %+v", rc)
	}
}

func TestBuild_ClaimAndVolumeUseTheFetchedObject(t *testing.T) {
	class := "fast"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "prod"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "disk", StorageClassName: &class}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class, ClaimRef: &corev1.ObjectReference{Namespace: "prod", Name: "data"}}}
	// The provider's inventories are still empty, as while claims and volumes load.
	opts := Options{Tier: TierBasic, Topology: &topology.Topology{}, Provider: mockResourceProvider{}}
	if rc := Build(context.Background(), pvc, opts); len(rc.Dependencies) != 2 {
		t.Fatalf("claim lost its declared volume and class: %+v", rc.Dependencies)
	}
	if rc := Build(context.Background(), pv, opts); len(rc.Dependencies) != 1 || len(rc.Dependents) != 1 {
		t.Fatalf("volume lost its declared class and claim: %+v", rc)
	}
}
