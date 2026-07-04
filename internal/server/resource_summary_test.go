package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// These tests pin the include=summary contract: every field the fleet GitOps
// board normalizers read (the keep-list) must survive the strip byte-identical,
// the heavy subtrees must be gone, and the input object must never be mutated.

func argoApplicationFixture() *unstructured.Unstructured {
	history := make([]any, 0, 10)
	for i := 0; i < 10; i++ {
		history = append(history, map[string]any{
			"id":              int64(i),
			"revision":        fmt.Sprintf("f3c9a1d7e5b2%028d", i),
			"deployStartedAt": fmt.Sprintf("2026-06-%02dT09:58:00Z", i+1),
			"deployedAt":      fmt.Sprintf("2026-06-%02dT10:00:00Z", i+1),
			"source": map[string]any{
				"repoURL":        "https://github.com/example-org/shop-backend-gitops",
				"path":           "environments/production/checkout-service",
				"targetRevision": "main",
			},
			"initiatedBy": map[string]any{"username": "argocd-image-updater-controller"},
		})
	}
	resources := make([]any, 0, 50)
	for i := 0; i < 50; i++ {
		resources = append(resources, map[string]any{
			"group":     "apps",
			"version":   "v1",
			"kind":      "Deployment",
			"namespace": "shop-backend-production",
			"name":      fmt.Sprintf("checkout-service-payment-gateway-worker-%02d", i),
			"status":    "Synced",
			"health": map[string]any{
				"status":  "Healthy",
				"message": "Deployment has minimum availability.",
			},
		})
	}
	syncResultResources := make([]any, 0, 50)
	for i := 0; i < 50; i++ {
		syncResultResources = append(syncResultResources, map[string]any{
			"group":     "apps",
			"version":   "v1",
			"kind":      "Deployment",
			"namespace": "shop-backend-production",
			"name":      fmt.Sprintf("checkout-service-payment-gateway-worker-%02d", i),
			"status":    "Synced",
			"message":   "deployment.apps/checkout-service-payment-gateway-worker configured",
			"hookPhase": "Succeeded",
			"syncPhase": "Sync",
		})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":              "checkout-service-production-us-east1",
			"namespace":         "argocd",
			"creationTimestamp": "2025-11-14T08:22:31Z",
			"deletionTimestamp": "2026-06-30T17:45:00Z",
			"labels": map[string]any{
				"app.kubernetes.io/instance": "checkout-service-production-us-east1",
				"team":                       "payments-platform",
			},
			"annotations": map[string]any{
				"radarhq.io/suspended-at":    "2026-06-30T12:00:00Z",
				"radarhq.io/suspended-by":    "nadav@example.com",
				"skyhook.io/suspended-at":    "2026-06-30T12:00:00Z",
				"skyhook.io/suspended-by":    "nadav@example.com",
				"argocd.argoproj.io/refresh": "normal",
			},
			"managedFields": []any{
				map[string]any{
					"manager":    "argocd-application-controller",
					"operation":  "Update",
					"apiVersion": "argoproj.io/v1alpha1",
					"time":       "2026-06-30T12:00:00Z",
					"fieldsType": "FieldsV1",
					"fieldsV1":   map[string]any{"f:status": map[string]any{"f:sync": map[string]any{}, "f:health": map[string]any{}, "f:resources": map[string]any{}, "f:history": map[string]any{}}},
				},
			},
		},
		"spec": map[string]any{
			"project": "shop-backend",
			"source": map[string]any{
				"repoURL":        "https://github.com/example-org/shop-backend-gitops",
				"targetRevision": "main",
				"path":           "environments/production/checkout-service",
				"chart":          "checkout-service",
			},
			"destination": map[string]any{
				"server":    "https://kubernetes.default.svc",
				"name":      "in-cluster",
				"namespace": "shop-backend-production",
			},
			"syncPolicy": map[string]any{
				"automated": map[string]any{"prune": true, "selfHeal": true},
			},
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "OutOfSync", "revision": "f3c9a1d7e5b2000000000000000000000000009"},
			"health": map[string]any{"status": "Degraded", "message": "Deployment exceeded its progress deadline"},
			"operationState": map[string]any{
				"phase":      "Failed",
				"message":    "one or more objects failed to apply",
				"finishedAt": "2026-06-10T10:00:12Z",
				"syncResult": map[string]any{
					"revision":  "f3c9a1d7e5b2000000000000000000000000009",
					"resources": syncResultResources,
					"source": map[string]any{
						"repoURL":        "https://github.com/example-org/shop-backend-gitops",
						"path":           "environments/production/checkout-service",
						"targetRevision": "main",
					},
				},
			},
			"reconciledAt": "2026-06-10T10:05:00Z",
			"history":      history,
			"resources":    resources,
		},
	}}
}

func fluxKustomizationFixture() *unstructured.Unstructured {
	entries := make([]any, 0, 50)
	for i := 0; i < 50; i++ {
		entries = append(entries, map[string]any{
			"id": fmt.Sprintf("shop-backend-production_checkout-service-payment-gateway-worker-%02d_apps_Deployment", i),
			"v":  "v1",
		})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata": map[string]any{
			"name":              "shop-backend-production-apps",
			"namespace":         "flux-system",
			"creationTimestamp": "2025-11-14T08:22:31Z",
			"labels":            map[string]any{"kustomize.toolkit.fluxcd.io/name": "shop-backend-production-apps"},
			"annotations":       map[string]any{"reconcile.fluxcd.io/requestedAt": "2026-06-10T10:00:00Z"},
			"managedFields": []any{
				map[string]any{"manager": "kustomize-controller", "operation": "Apply", "fieldsType": "FieldsV1", "fieldsV1": map[string]any{"f:status": map[string]any{"f:inventory": map[string]any{}}}},
			},
		},
		"spec": map[string]any{
			"sourceRef":       map[string]any{"name": "shop-backend-gitops", "kind": "GitRepository"},
			"path":            "./environments/production",
			"targetNamespace": "shop-backend-production",
			"suspend":         true,
			"interval":        "5m",
			"prune":           true,
		},
		"status": map[string]any{
			"lastAppliedRevision":   "main@sha1:f3c9a1d7e5b2000000000000000000000000009",
			"lastAttemptedRevision": "main@sha1:a1b2c3d4e5f6000000000000000000000000010",
			"conditions": []any{
				map[string]any{
					"type":               "Ready",
					"status":             "False",
					"reason":             "BuildFailed",
					"message":            "kustomize build failed: accumulating resources from 'base': missing kustomization.yaml",
					"lastTransitionTime": "2026-06-10T10:00:05Z",
				},
				map[string]any{
					"type":               "Reconciling",
					"status":             "True",
					"reason":             "ProgressingWithRetry",
					"message":            "reconciliation in progress",
					"lastTransitionTime": "2026-06-10T10:00:05Z",
				},
			},
			"inventory": map[string]any{"entries": entries},
		},
	}}
}

func fluxHelmReleaseFixture() *unstructured.Unstructured {
	entries := make([]any, 0, 30)
	for i := 0; i < 30; i++ {
		entries = append(entries, map[string]any{
			"id": fmt.Sprintf("observability-stack_prometheus-kube-state-metrics-shard-%02d_apps_StatefulSet", i),
			"v":  "v1",
		})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":              "kube-prometheus-stack-observability",
			"namespace":         "observability-stack",
			"creationTimestamp": "2025-11-14T08:22:31Z",
			"labels":            map[string]any{"app.kubernetes.io/managed-by": "helm-controller"},
			"annotations":       map[string]any{"reconcile.fluxcd.io/requestedAt": "2026-06-10T10:00:00Z"},
			"managedFields": []any{
				map[string]any{"manager": "helm-controller", "operation": "Apply", "fieldsType": "FieldsV1", "fieldsV1": map[string]any{"f:status": map[string]any{}}},
			},
		},
		"spec": map[string]any{
			"chart": map[string]any{
				"spec": map[string]any{
					"sourceRef": map[string]any{"name": "prometheus-community-helm-charts", "kind": "HelmRepository"},
					"version":   "58.2.1",
					"chart":     "kube-prometheus-stack",
				},
			},
			"targetNamespace": "observability-stack",
			"suspend":         false,
			"interval":        "10m",
		},
		"status": map[string]any{
			"lastAttemptedRevision": "58.2.1",
			"conditions": []any{
				map[string]any{
					"type":               "Ready",
					"status":             "True",
					"reason":             "InstallSucceeded",
					"message":            "Helm install succeeded for release observability-stack/kube-prometheus-stack-observability.v1",
					"lastTransitionTime": "2026-06-10T10:00:05Z",
				},
			},
			"inventory": map[string]any{"entries": entries},
		},
	}}
}

func mustNested(t *testing.T, obj map[string]any, path ...string) any {
	t.Helper()
	v, found, err := unstructured.NestedFieldNoCopy(obj, path...)
	if err != nil {
		t.Fatalf("path %v: %v", path, err)
	}
	if !found {
		t.Fatalf("path %v missing after strip", path)
	}
	return v
}

func assertPathsPreserved(t *testing.T, original, stripped *unstructured.Unstructured, paths [][]string) {
	t.Helper()
	for _, path := range paths {
		want := mustNested(t, original.Object, path...)
		got := mustNested(t, stripped.Object, path...)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("path %v changed: got %v, want %v", path, got, want)
		}
	}
}

func assertPathAbsent(t *testing.T, obj map[string]any, path ...string) {
	t.Helper()
	if _, found, _ := unstructured.NestedFieldNoCopy(obj, path...); found {
		t.Errorf("path %v should have been stripped", path)
	}
}

// stripViaList strips a COPY of the fixture through the handler-shaped path
// (applySummaryStrip is in-place; the handler always feeds owned copies).
// Returns (stripped, pristineSnapshot) so callers assert keep-list fields
// survived against the untouched original.
func stripViaList(t *testing.T, fixture *unstructured.Unstructured) (*unstructured.Unstructured, *unstructured.Unstructured) {
	t.Helper()
	snapshot := fixture.DeepCopy()
	target := fixture.DeepCopy()
	applySummaryStrip([]*unstructured.Unstructured{target})
	return target, snapshot
}

func TestSummaryStripArgoApplication(t *testing.T) {
	fixture := argoApplicationFixture()

	stripped, snapshot := stripViaList(t, fixture)

	assertPathsPreserved(t, snapshot, stripped, [][]string{
		{"metadata", "name"},
		{"metadata", "namespace"},
		{"metadata", "labels"},
		{"metadata", "creationTimestamp"},
		{"metadata", "deletionTimestamp"},
		{"metadata", "annotations"},
		{"spec", "project"},
		{"spec", "source", "repoURL"},
		{"spec", "source", "targetRevision"},
		{"spec", "source", "path"},
		{"spec", "source", "chart"},
		{"spec", "destination", "server"},
		{"spec", "destination", "name"},
		{"spec", "destination", "namespace"},
		{"spec", "syncPolicy", "automated"},
		{"status", "sync", "status"},
		{"status", "health", "status"},
		{"status", "health", "message"},
		{"status", "operationState", "phase"},
		{"status", "operationState", "message"},
		{"status", "operationState", "finishedAt"},
		{"status", "reconciledAt"},
	})

	assertPathAbsent(t, stripped.Object, "status", "resources")
	assertPathAbsent(t, stripped.Object, "status", "operationState", "syncResult")
	assertPathAbsent(t, stripped.Object, "metadata", "managedFields")

	history := mustNested(t, stripped.Object, "status", "history").([]any)
	if len(history) != 1 {
		t.Fatalf("history should be trimmed to the last entry, got %d entries", len(history))
	}
	origHistory := mustNested(t, snapshot.Object, "status", "history").([]any)
	origLast := origHistory[len(origHistory)-1].(map[string]any)
	want := map[string]any{"deployedAt": origLast["deployedAt"]}
	if !reflect.DeepEqual(history[0], want) {
		t.Errorf("history[last] should keep only deployedAt: got %v, want %v", history[0], want)
	}
}

func TestSummaryStripArgoApplicationSparse(t *testing.T) {
	sparse := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "bare-app", "namespace": "argocd"},
		"spec":       map[string]any{"project": "default"},
	}}
	stripped, _ := stripViaList(t, sparse)
	if got := mustNested(t, stripped.Object, "metadata", "name"); got != "bare-app" {
		t.Errorf("metadata.name = %v", got)
	}
	assertPathAbsent(t, stripped.Object, "status", "history")
}

func TestSummaryStripFluxKustomization(t *testing.T) {
	fixture := fluxKustomizationFixture()

	stripped, snapshot := stripViaList(t, fixture)

	assertPathsPreserved(t, snapshot, stripped, [][]string{
		{"metadata", "name"},
		{"metadata", "namespace"},
		{"metadata", "labels"},
		{"metadata", "creationTimestamp"},
		{"metadata", "annotations"},
		{"spec", "sourceRef", "name"},
		{"spec", "sourceRef", "kind"},
		{"spec", "path"},
		{"spec", "targetNamespace"},
		{"spec", "suspend"},
		{"status", "lastAppliedRevision"},
		{"status", "lastAttemptedRevision"},
		{"status", "conditions"},
	})

	assertPathAbsent(t, stripped.Object, "status", "inventory")
	assertPathAbsent(t, stripped.Object, "metadata", "managedFields")
}

func TestSummaryStripFluxHelmRelease(t *testing.T) {
	fixture := fluxHelmReleaseFixture()

	stripped, snapshot := stripViaList(t, fixture)

	assertPathsPreserved(t, snapshot, stripped, [][]string{
		{"metadata", "name"},
		{"metadata", "namespace"},
		{"metadata", "labels"},
		{"metadata", "creationTimestamp"},
		{"metadata", "annotations"},
		{"spec", "chart", "spec", "sourceRef", "name"},
		{"spec", "chart", "spec", "sourceRef", "kind"},
		{"spec", "chart", "spec", "version"},
		{"spec", "chart", "spec", "chart"},
		{"spec", "targetNamespace"},
		{"spec", "suspend"},
		{"status", "lastAttemptedRevision"},
		{"status", "conditions"},
	})

	assertPathAbsent(t, stripped.Object, "status", "inventory")
	assertPathAbsent(t, stripped.Object, "metadata", "managedFields")
}

func TestSummaryStripUnprofiledKindPassesThrough(t *testing.T) {
	gitRepo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata":   map[string]any{"name": "shop-backend-gitops", "namespace": "flux-system"},
		"spec":       map[string]any{"url": "https://github.com/example-org/shop-backend-gitops"},
		"status":     map[string]any{"artifact": map[string]any{"revision": "main@sha1:f3c9a1d7"}},
	}}
	stripped, snapshot := stripViaList(t, gitRepo)
	if !reflect.DeepEqual(stripped.Object, snapshot.Object) {
		t.Error("kind without a strip profile must be returned unchanged")
	}
}

func TestSummaryStripMergedAnySlice(t *testing.T) {
	app := argoApplicationFixture()
	gitRepo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata":   map[string]any{"name": "shop-backend-gitops", "namespace": "flux-system"},
	}}
	appCopy := app.DeepCopy()
	out, ok := applySummaryStrip([]any{appCopy, gitRepo}).([]any)
	if !ok || len(out) != 2 {
		t.Fatalf("unexpected shape: %T", out)
	}
	strippedApp := out[0].(*unstructured.Unstructured)
	assertPathAbsent(t, strippedApp.Object, "status", "resources")
	if out[1] != any(gitRepo) {
		t.Error("unprofiled item in []any must pass through unchanged")
	}
}

func TestSummaryStripNonUnstructuredResultUnchanged(t *testing.T) {
	result := []any{"not-an-unstructured"}
	out := applySummaryStrip(result).([]any)
	if out[0] != "not-an-unstructured" {
		t.Error("non-unstructured items must pass through unchanged")
	}
	if got := applySummaryStrip(42); got != 42 {
		t.Error("non-slice results must pass through unchanged")
	}
}

func TestSummaryStripSizeReduction(t *testing.T) {
	fixture := argoApplicationFixture()
	before, err := json.Marshal(fixture.Object)
	if err != nil {
		t.Fatal(err)
	}
	stripped, _ := stripViaList(t, fixture)
	after, err := json.Marshal(stripped.Object)
	if err != nil {
		t.Fatal(err)
	}
	if len(after)*10 >= len(before)*3 {
		t.Errorf("summary should shrink the heavy Application below 30%%: before=%d after=%d (%.1f%%)",
			len(before), len(after), float64(len(after))*100/float64(len(before)))
	}
}
