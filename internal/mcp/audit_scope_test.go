package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAuditToolWithholdsSecretFindingsAndCounts(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "private-secret", Namespace: "app", DeletionTimestamp: &metav1.Time{Time: time.Now().Add(-time.Hour)}, Finalizers: []string{"example.com/hold"}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(secret)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	for _, allowed := range []bool{true, false, true} {
		ctx := withTestUserPerms(t, "audit-reader", nil, []string{"app"})
		getPermCache().Get("audit-reader", nil).SetCanI("list", "", "secrets", "app", allowed)
		raw, _, err := handleGetAudit(ctx, nil, auditInput{Namespace: "app"})
		if err != nil {
			t.Fatal(err)
		}
		var result auditToolResult
		if err := json.Unmarshal([]byte(extractText(t, raw)), &result); err != nil {
			t.Fatal(err)
		}
		summary := computeMCPAuditSummary(ctx, k8s.GetResourceCache(), "", "Secret", "app", "private-secret")
		if summary == nil || (summary.Count > 0) != allowed || slices.Contains(summary.MissingInputs, "secrets") == allowed {
			t.Fatal("MCP resource summary diverged from audit grant")
		}
		if slices.Contains(result.MissingInputs, "secrets") == allowed {
			t.Fatal("MCP lost missing Secret inputs")
		}
		if allowed && result.TotalCount == 0 {
			t.Fatal("authorized Secret finding missing")
		}
		if !allowed {
			if result.TotalCount != 0 || result.Summary.Resources != 0 || len(result.Findings) != 0 {
				t.Fatalf("hidden Secret disclosed: %+v", result)
			}

		}
	}
}

func TestAuditToolStoragePVGrantOnlyGatesPVMetadataAndFindings(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "app", CreationTimestamp: metav1.NewTime(time.Now().Add(-48 * time.Hour))}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "disk"}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk", CreationTimestamp: metav1.NewTime(time.Now().Add(-48 * time.Hour))}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pvc, pv)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	for _, allowed := range []bool{true, false, true} {
		ctx := withTestUserPerms(t, "storage-audit-reader", nil, []string{"app"})
		getPermCache().Get("storage-audit-reader", nil).SetCanI("list", "", "persistentvolumes", "", allowed)
		getPermCache().Get("storage-audit-reader", nil).SetCanI("list", "storage.k8s.io", "storageclasses", "", false)
		raw, _, err := handleGetAudit(ctx, nil, auditInput{Namespace: "app", Category: "Efficiency"})
		if err != nil {
			t.Fatal(err)
		}
		var result auditToolResult
		if err := json.Unmarshal([]byte(extractText(t, raw)), &result); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(result.MissingInputs, "persistentvolumes") == allowed {
			t.Fatalf("PV availability differs from grant: %+v", result)
		}
		want := 1
		if allowed {
			want = 2
		}
		if result.TotalCount != want || result.Summary.Medium != want || result.Summary.Resources != want {
			t.Fatalf("storage counts differ from grant: %+v", result)
		}
		for _, f := range result.Findings {
			if !allowed && (f.Check != "pvcNoConsumer" || !strings.Contains(f.Message, "reclaim policy not visible")) {
				t.Fatalf("unauthorized PV metadata/finding: %+v", f)
			}
			if f.Check != "pvcNoConsumer" && f.Check != "releasedPV" || f.Remediation == "" {
				t.Fatalf("unexpected finding: %+v", f)
			}
		}
	}
}

func TestAuditToolPreservesAuthorizedClusterScopedFindings(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "database.example.com", Version: "v1", Resource: "databases"}
	mr := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "database.example.com/v1", "kind": "Database", "metadata": map[string]any{"name": "private-db"}, "spec": map[string]any{"providerConfigRef": map[string]any{"name": "default"}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": time.Now().Add(-time.Hour).Format(time.RFC3339)}}}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "DatabaseList"}, mr)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Database", Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dynamic := k8s.GetDynamicResourceCache()
	if err := dynamic.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dynamic.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("sync timeout")
	}
	for _, allowed := range []bool{true, false, true} {
		ctx := withTestUserPerms(t, "cluster-audit-reader", nil, []string{"app"})
		getPermCache().Get("cluster-audit-reader", nil).SetCanI("list", gvr.Group, gvr.Resource, "", allowed)
		raw, _, err := handleGetAudit(ctx, nil, auditInput{})
		if err != nil {
			t.Fatal(err)
		}
		var result auditToolResult
		if err := json.Unmarshal([]byte(extractText(t, raw)), &result); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range result.Findings {
			if f.Check == "crossplaneStuck" {
				found = true
			}
		}
		if !allowed && (result.TotalCount != 0 || result.Summary.Resources != 0) {
			t.Fatal("unauthorized cluster subject remains counted")
		}
		if found != allowed {
			t.Fatalf("cluster grant=%v, finding=%v", allowed, found)
		}
	}
}

func TestAuditGlobalViewerRetainsNamespaceSecretGrant(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "allowed"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "hidden"}},
	)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), []k8s.APIResource{{Version: "v1", Name: "pods", Kind: "Pod", Namespaced: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	ctx := withTestUserPerms(t, "viewer", nil, nil)
	perms := getPermCache().Get("viewer", nil)
	perms.SetCanI("list", "", "secrets", "", false)
	perms.SetCanI("list", "", "secrets", "app", true)
	perms.SetCanI("list", "", "secrets", "other", false)
	raw, _, err := handleGetAudit(ctx, nil, auditInput{})
	if err != nil {
		t.Fatal(err)
	}
	var result auditToolResult
	if err := json.Unmarshal([]byte(extractText(t, raw)), &result); err != nil {
		t.Fatal(err)
	}
	if result.TotalCount != 1 || result.Findings[0].Resource != "Secret/app/allowed" {
		t.Fatalf("namespace grant not honored: %+v", result)
	}
}
