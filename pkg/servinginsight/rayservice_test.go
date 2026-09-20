package servinginsight

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"os"
	"reflect"
	"sigs.k8s.io/yaml"
	"strings"
	"testing"
)

func newRayService() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ray.io/v1", "kind": "RayService",
		"metadata": map[string]any{"name": "serve", "namespace": "ml", "generation": int64(4)},
	}}
}
func condition(kind, status, reason string) map[string]any {
	return map[string]any{"type": kind, "status": status, "reason": reason, "observedGeneration": int64(3), "message": "native evidence", "lastTransitionTime": "2026-09-20T00:00:00Z"}
}
func fixture(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile("testdata/rayservice-rollback.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data, err = yaml.YAMLToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{}
	if err = obj.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	return obj
}
func wire(t *testing.T, obj *unstructured.Unstructured, tier resourcecontext.ContextTier) string {
	t.Helper()
	data, err := json.Marshal(resourcecontext.ResourceContext{Tier: tier, RayServiceSummary: ForRayService(obj)})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExactGVKAndAbsentState(t *testing.T) {
	for _, gvk := range []struct{ version, kind string }{{"ray.io/v1alpha1", "RayService"}, {"other.io/v1", "RayService"}, {"ray.io/v1", "RayCluster"}, {"ray.io/v1", "RayJob"}} {
		obj := newRayService()
		obj.SetAPIVersion(gvk.version)
		obj.SetKind(gvk.kind)
		if got := ForRayService(obj); got != nil {
			t.Fatalf("unsupported %v: %+v", gvk, got)
		}
	}
	for _, obj := range []runtime.Object{nil, &corev1.Service{}} {
		if ForRayService(obj) != nil {
			t.Fatal("unsupported object")
		}
	}
	obj := newRayService()
	want := `{"tier":"basic","rayServiceSummary":{"subjectGeneration":4,"suspendRequested":false}}`
	if got := wire(t, obj, resourcecontext.TierBasic); got != want {
		t.Fatalf("absent status invented facts: %s", got)
	}
	obj.Object["status"] = map[string]any{"serviceStatus": "Running"}
	if got := wire(t, obj, resourcecontext.TierBasic); got != want {
		t.Fatalf("deprecated state inferred facts: %s", got)
	}
}

func TestBuildKeepsIndependentConditionsAndIntent(t *testing.T) {
	cases := []struct {
		name       string
		suspend    bool
		conditions []any
	}{
		{"healthy during upgrade", false, []any{condition("Ready", "True", "NonZeroServeEndpoints"), condition("UpgradeInProgress", "True", "BothActivePendingClustersExist")}},
		{"rollback does not erase serving", false, []any{condition("Ready", "True", "NonZeroServeEndpoints"), condition("UpgradeInProgress", "True", "BothActivePendingClustersExist"), condition("RollbackInProgress", "True", "DesiredClusterSpecChanged")}},
		{"request without acknowledgement", true, []any{condition("Ready", "True", "NonZeroServeEndpoints")}},
		{"atomic suspending after resume requested", false, []any{condition("Suspending", "True", "SuspendInProgress")}},
		{"suspended after resume requested", false, []any{condition("Suspended", "True", "SuspendComplete")}},
		{"validation failure after spec edit", true, []any{condition("Ready", "False", "ValidationFailed"), condition("Suspended", "True", "SuspendComplete")}},
		{"initialization timeout", false, []any{condition("Ready", "False", "InitializingTimeout")}},
		{"unknown is not false", false, []any{condition("UpgradeInProgress", "Unknown", "NoActiveCluster")}},
		{"completed upgrade", false, []any{condition("Ready", "True", "NonZeroServeEndpoints"), condition("UpgradeInProgress", "False", "NoPendingCluster")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := newRayService()
			obj.Object["spec"] = map[string]any{"suspend": tc.suspend}
			obj.Object["status"] = map[string]any{"observedGeneration": int64(3), "conditions": tc.conditions}
			before := obj.DeepCopy()
			projected := ForRayService(obj)
			got := resourcecontext.Build(context.Background(), obj, resourcecontext.Options{Tier: resourcecontext.TierBasic, RayServiceSummary: projected})
			if got.RayServiceSummary != projected {
				t.Fatalf("context wiring: %+v", got)
			}
			if projected.SubjectGeneration != 4 || projected.ObservedGeneration != 3 || projected.SuspendRequested != tc.suspend {
				t.Fatalf("intent/generation lost: %+v", projected)
			}
			if got.StatusSummary == nil || len(got.StatusSummary.Conditions) != len(tc.conditions) {
				t.Fatalf("conditions lost: %+v", got)
			}
			for i, raw := range tc.conditions {
				want := raw.(map[string]any)
				c := got.StatusSummary.Conditions[i]
				if c.Type != want["type"] || c.Status != want["status"] || c.Reason != want["reason"] || c.ObservedGeneration != 3 {
					t.Fatalf("condition changed: %+v", c)
				}
			}
			if !reflect.DeepEqual(obj, before) {
				t.Fatal("projection mutated input")
			}
			now := metav1.Now()
			obj.SetDeletionTimestamp(&now)
			if after := ForRayService(obj); !reflect.DeepEqual(projected, after) {
				t.Fatal("deletion reclassified native state")
			}
		})
	}
}

func TestRuntimeApplicationsAndPercentages(t *testing.T) {
	obj := fixture(t)
	got := ForRayService(obj)
	if got.Active.ClusterName != "image-service-raycluster-old" || got.Pending.ClusterName != "image-service-raycluster-new" {
		t.Fatalf("runtime identity: %+v", got)
	}
	if *got.Active.TargetCapacityPercent != 100 || *got.Active.TrafficRoutedPercent != 65 || *got.Pending.TargetCapacityPercent != 0 || *got.Pending.TrafficRoutedPercent != 0 {
		t.Fatal("percentages changed")
	}
	if !reflect.DeepEqual(got.Active.Applications, []resourcecontext.ServeApplicationStatus{{Name: "image", Status: "RUNNING"}}) || !reflect.DeepEqual(got.Pending.Applications, []resourcecontext.ServeApplicationStatus{{Name: "image", Status: "DEPLOY_FAILED"}}) {
		t.Fatalf("application states: %+v %+v", got.Active, got.Pending)
	}
	for _, tier := range []resourcecontext.ContextTier{resourcecontext.TierBasic, resourcecontext.TierDiagnostic} {
		json := wire(t, obj, tier)
		for _, excluded := range []string{"primaryCondition", "HeadPodReady", "ReplicaFailure", "serveDeploymentStatuses", "traceback", "state"} {
			if strings.Contains(json, `"`+excluded+`"`) {
				t.Fatalf("copied excluded detail: %s", json)
			}
		}
	}
}

func TestPartialRuntimeDoesNotInventChildState(t *testing.T) {
	obj := newRayService()
	obj.Object["status"] = map[string]any{
		"activeServiceStatus":  map[string]any{"rayClusterName": "active", "rayClusterStatus": map[string]any{"state": "failed", "conditions": []any{condition("HeadPodReady", "True", "HeadPodRunningAndReady")}}},
		"pendingServiceStatus": map[string]any{"rayClusterName": "pending"},
	}
	got := ForRayService(obj)
	want := &resourcecontext.RayServiceSummary{SubjectGeneration: 4, Active: &resourcecontext.RayServiceRuntime{ClusterName: "active"}, Pending: &resourcecontext.RayServiceRuntime{ClusterName: "pending"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invented missing evidence: %+v", got)
	}
	delete(obj.Object["status"].(map[string]any)["pendingServiceStatus"].(map[string]any), "rayClusterName")
	_ = unstructured.SetNestedField(obj.Object, int64(0), "status", "pendingServiceStatus", "targetCapacity")
	if ForRayService(obj).Pending != nil {
		t.Fatal("unnamed runtime emitted")
	}
	// A reported app can itself have no status; do not fill it with RUNNING.
	_ = unstructured.SetNestedMap(obj.Object, map[string]any{"unobserved": map[string]any{}}, "status", "activeServiceStatus", "applicationStatuses")
	if apps := ForRayService(obj).Active.Applications; len(apps) != 1 || apps[0].Status != "" {
		t.Fatalf("invented app state: %+v", apps)
	}
}

func TestDeterministicCardinalityAndOutputBudget(t *testing.T) {
	obj := fixture(t)
	apps := map[string]any{}
	for i := 0; i < 1000; i++ {
		apps[fmt.Sprintf("app-%04d", i)] = map[string]any{"status": "UNRECOGNIZED_NATIVE_STATE", "message": strings.Repeat("界", 10000), "serveDeploymentStatuses": map[string]any{"huge": strings.Repeat("irrelevant", 10000)}}
	}
	for _, slot := range []string{"activeServiceStatus", "pendingServiceStatus"} {
		_ = unstructured.SetNestedMap(obj.Object, apps, "status", slot, "applicationStatuses")
	}
	got := ForRayService(obj)
	for _, slot := range []*resourcecontext.RayServiceRuntime{got.Active, got.Pending} {
		if !slot.ApplicationsTruncated || len(slot.Applications) != 8 || slot.Applications[0].Name != "app-0000" || slot.Applications[7].Name != "app-0007" || slot.Applications[0].Status != "UNRECOGNIZED_NATIVE_STATE" {
			t.Fatalf("cap or native values: %+v", slot)
		}
	}
	baseline := wire(t, obj, resourcecontext.TierBasic)
	if len(baseline) > 1800 {
		t.Fatalf("summary exceeded budget: %d", len(baseline))
	}
	if again := wire(t, obj, resourcecontext.TierBasic); again != baseline {
		t.Fatal("nondeterministic output")
	}
	t.Logf("1000 applications per slot project to %d bytes", len(baseline))
	for i := 8; i < 1000; i++ {
		delete(apps, fmt.Sprintf("app-%04d", i))
	}
	_ = unstructured.SetNestedMap(obj.Object, apps, "status", "activeServiceStatus", "applicationStatuses")
	if ForRayService(obj).Active.ApplicationsTruncated {
		t.Fatal("exactly eight apps incorrectly truncated")
	}
}

func TestDeclaredUpgradeStrategyDoesNotInferDefaults(t *testing.T) {
	for _, strategy := range []string{"", "None", "NewCluster", "NewClusterWithIncrementalUpgrade", "FutureStrategy"} {
		obj := newRayService()
		if strategy != "" {
			_ = unstructured.SetNestedField(obj.Object, strategy, "spec", "upgradeStrategy", "type")
		}
		if got := ForRayService(obj).UpgradeStrategy; got != strategy {
			t.Fatalf("declared %q became %q", strategy, got)
		}
	}
}
