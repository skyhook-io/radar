package k8s

import (
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestDetectCAPIMachineConditionAuthority(t *testing.T) {
	defer ResetTestDynamicState()
	now := time.Now().UTC().Truncate(time.Second)
	onset := now.Add(-30 * time.Second)
	condition := func(typ, status, reason string, at time.Time) map[string]any {
		return map[string]any{"type": typ, "status": status, "reason": reason, "message": reason + " detail", "lastTransitionTime": at.Format(time.RFC3339)}
	}
	objs := []runtime.Object{}
	wants := map[string]Detection{}
	quiet := []string{}
	gvr := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "machines"}
	resources := []APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Machine", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch"}}}
	lists := map[schema.GroupVersionResource]string{gvr: "MachineList"}
	for _, status := range []string{"True", "Unknown", "False"} {
		name := "machine-modern-" + strings.ToLower(status)
		obj := capiObj(gvr.Group, "Machine", name, map[string]any{
			"phase": "Running", "observedGeneration": int64(1),
			"v1beta2":    map[string]any{"conditions": []any{condition("Ready", status, "CurrentSummary", onset), condition("InfrastructureReady", "True", "Ready", onset), condition("NodeHealthy", status, "CurrentObservation", onset)}},
			"conditions": []any{condition("Ready", "True", "Ready", onset), condition("InfrastructureReady", "True", "Ready", onset), condition("NodeHealthy", "False", "DeprecatedFailure", now.Add(-time.Hour))},
		})
		obj.SetCreationTimestamp(metav1.NewTime(now.Add(-time.Minute)))
		objs = append(objs, obj)
		if status == "Unknown" {
			wants[name] = Detection{Reason: "DeprecatedFailure", Message: "DeprecatedFailure detail", Severity: "high", OnsetAt: now.Add(-time.Hour)}
		} else if status == "False" {
			wants[name] = Detection{Reason: "CurrentObservation", Message: "CurrentObservation detail", Severity: "high", OnsetAt: onset}
		} else {
			quiet = append(quiet, name)
		}
	}
	machine := func(name string, status map[string]any) *unstructured.Unstructured {
		obj := capiObj("cluster.x-k8s.io", "Machine", name, status)
		obj.SetCreationTimestamp(metav1.NewTime(now.Add(-time.Minute)))
		objs = append(objs, obj)
		return obj
	}
	progress := condition("InfrastructureReady", "False", "Creating", onset)
	legacyWaiting := condition("NodeHealthy", "False", "WaitingForNodeRef", onset)
	legacyWaiting["severity"] = "Info"
	machine("legacy-progress-first-unchanged", map[string]any{"phase": "Provisioning", "conditions": []any{progress, legacyWaiting}})
	machine("progress-first-unchanged", map[string]any{"phase": "Running", "v1beta2": map[string]any{"conditions": []any{progress, condition("NodeHealthy", "False", "NodeNotHealthy", onset), condition("Ready", "False", "NotReady", onset)}}})
	machine("bridged-node-unknown", map[string]any{"phase": "Provisioning", "v1beta2": map[string]any{"conditions": []any{condition("Ready", "Unknown", "ReadyUnknown", onset), condition("InfrastructureReady", "True", "Ready", onset), condition("NodeHealthy", "Unknown", "InspectionFailed", onset)}}, "conditions": []any{condition("Ready", "True", "Ready", onset), legacyWaiting}})
	quiet = append(quiet, "legacy-progress-first-unchanged", "progress-first-unchanged")
	wants["bridged-node-unknown"] = Detection{Reason: "WaitingForNodeRef", Message: "WaitingForNodeRef detail", Severity: "high", OnsetAt: onset}
	neverJoined := machine("node-never-joined", map[string]any{"phase": "Provisioned", "v1beta2": map[string]any{"conditions": []any{condition("Ready", "Unknown", "ReadyUnknown", onset), condition("NodeHealthy", "Unknown", "InspectionFailed", onset)}}, "conditions": []any{condition("Ready", "True", "Ready", onset), condition("NodeHealthy", "False", "NodeProvisioning", onset)}})
	neverJoined.SetCreationTimestamp(metav1.NewTime(now.Add(-time.Hour)))
	neverJoined.Object["spec"] = map[string]any{"providerID": "test://machine"}
	wants["node-never-joined"] = Detection{Reason: "NodeProvisioning", Message: "NodeProvisioning detail", Severity: "high", OnsetAt: onset}
	machine("modern-ready-failure", map[string]any{"phase": "Running", "v1beta2": map[string]any{"conditions": []any{condition("Ready", "False", "NotReady", onset), condition("NodeHealthy", "True", "Healthy", onset)}}, "conditions": []any{condition("Ready", "False", "DeprecatedSummary", now.Add(-time.Hour)), condition("NodeHealthy", "False", "DeprecatedFailure", now.Add(-time.Hour))}})
	wants["modern-ready-failure"] = Detection{Reason: "NotReady", Message: "NotReady detail", Severity: "high", OnsetAt: onset}
	machine("resolved-progress-stays-quiet", map[string]any{"phase": "Provisioning", "v1beta2": map[string]any{"conditions": []any{condition("Ready", "Unknown", "ReadyUnknown", onset), condition("InfrastructureReady", "True", "Ready", onset), condition("NodeHealthy", "Unknown", "InspectionFailed", onset)}}, "conditions": []any{condition("Ready", "False", "Creating", onset), progress, legacyWaiting}})
	quiet = append(quiet, "resolved-progress-stays-quiet")
	missing := machine("missing-modern-type", map[string]any{"phase": "Running", "v1beta2": map[string]any{"conditions": []any{condition("InfrastructureReady", "True", "Ready", onset)}}, "conditions": []any{condition("InfrastructureReady", "False", "DeprecatedFailure", onset), condition("NodeHealthy", "False", "NodeNotHealthy", onset)}})
	wants[missing.GetName()] = Detection{Reason: "NodeNotHealthy", Message: "NodeNotHealthy detail", Severity: "high", OnsetAt: onset}
	outside := missing.DeepCopy()
	outside.SetNamespace("outside")
	outside.SetName("outside-failure")
	objs = append(objs, outside)
	noDetail := func(phase string) map[string]any {
		return map[string]any{"phase": phase, "v1beta2": map[string]any{"conditions": []any{condition("InfrastructureReady", "True", "Ready", onset)}}, "conditions": []any{condition("InfrastructureReady", "False", "DeprecatedDetail", onset)}}
	}
	machine("failed-detail", noDetail("Failed"))
	wants["failed-detail"] = Detection{Reason: "Machine in Failed phase", Severity: "critical"}
	terminal := noDetail("Failed")
	terminal["failureMessage"] = "terminal failure detail"
	machine("terminal-detail", terminal)
	wants["terminal-detail"] = Detection{Reason: "Machine in Failed phase", Message: "terminal failure detail", Severity: "critical"}
	stuck := machine("stuck-detail", noDetail("Provisioning"))
	stuck.SetCreationTimestamp(metav1.NewTime(now.Add(-11 * time.Minute)))
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, objs...)
	if err := InitTestDynamicResourceCache(client, resources); err != nil {
		t.Fatal(err)
	}
	cache, discovery := GetDynamicResourceCache(), GetResourceDiscovery()
	before := map[string]any{}
	for gvr := range lists {
		items, err := cache.ListBlocking(gvr, "capi-system", 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			before[item.GetName()] = item.DeepCopy().Object
		}
	}
	byName := map[string]Detection{}
	for _, d := range DetectCAPIProblems(cache, discovery, "capi-system") {
		byName[d.Name] = d
	}
	for _, name := range quiet {
		if got, ok := byName[name]; ok {
			t.Errorf("%s should remain quiet: %+v", name, got)
		}
	}
	for name, want := range wants {
		got, ok := byName[name]
		if !ok || got.Reason != want.Reason || got.Message != want.Message || got.Severity != want.Severity || !got.OnsetAt.Equal(want.OnsetAt) {
			t.Errorf("%s = %+v; want reason=%q message=%q severity=%q onset=%v", name, got, want.Reason, want.Message, want.Severity, want.OnsetAt)
		}
	}
	if got, ok := byName["stuck-detail"]; !ok || !strings.HasPrefix(got.Reason, "Stuck provisioning") || strings.Contains(got.Reason, "DeprecatedDetail") || got.Message != "" || !got.OnsetAt.Equal(stuck.GetCreationTimestamp().Time) {
		t.Errorf("stuck phase must retain priority without deprecated detail: %+v", got)
	}
	if got, ok := byName["outside-failure"]; ok {
		t.Errorf("namespace scope leaked: %+v", got)
	}
	for gvr := range lists {
		items, err := cache.ListBlocking(gvr, "capi-system", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if !reflect.DeepEqual(before[item.GetName()], item.Object) {
				t.Errorf("raw resource %s changed", item.GetName())
			}
		}
	}
}

func TestCAPIMachineFalseConditionSources(t *testing.T) {
	condition := func(typ, status, reason string) map[string]any {
		return map[string]any{"type": typ, "status": status, "reason": reason}
	}
	tests := []struct {
		name, version        string
		modern, legacy       []any
		types                []string
		wantType, wantReason string
	}{
		{name: "modern true", modern: []any{condition("NodeHealthy", "True", "Healthy")}, legacy: []any{condition("NodeHealthy", "False", "Deprecated")}, types: []string{"NodeHealthy"}},
		{name: "modern true and exhausted types do not default to Ready", modern: []any{condition("NodeHealthy", "True", "Healthy")}, legacy: []any{condition("NodeHealthy", "False", "Deprecated"), condition("Ready", "False", "NotReady")}, types: []string{"NodeHealthy"}},
		{name: "modern unknown retains legacy failure", modern: []any{condition("NodeHealthy", "Unknown", "InspectionFailed")}, legacy: []any{condition("NodeHealthy", "False", "NodeProvisioning")}, types: []string{"NodeHealthy"}, wantType: "NodeHealthy", wantReason: "NodeProvisioning"},
		{name: "modern false", modern: []any{condition("NodeHealthy", "False", "CurrentFailure")}, legacy: []any{condition("NodeHealthy", "False", "Deprecated")}, types: []string{"NodeHealthy"}, wantType: "NodeHealthy", wantReason: "CurrentFailure"},
		{name: "missing modern type", modern: []any{condition("Ready", "True", "Ready")}, legacy: []any{condition("NodeHealthy", "False", "NodeNotHealthy")}, types: []string{"NodeHealthy"}, wantType: "NodeHealthy", wantReason: "NodeNotHealthy"},
		{name: "legacy object order", legacy: []any{condition("NodeHealthy", "False", "First"), condition("InfrastructureReady", "False", "Second")}, types: []string{"InfrastructureReady", "NodeHealthy"}, wantType: "NodeHealthy", wantReason: "First"},
		{name: "no requested types", legacy: []any{condition("Ready", "False", "NotReady")}},
		{name: "direct v1beta2", version: "v1beta2", legacy: []any{condition("NodeHealthy", "False", "ModernFailure")}, types: []string{"NodeHealthy"}, wantType: "NodeHealthy", wantReason: "ModernFailure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := capiObj("cluster.x-k8s.io", "Machine", "authority", map[string]any{"conditions": tt.legacy})
			if tt.modern != nil {
				obj.Object["status"].(map[string]any)["v1beta2"] = map[string]any{"conditions": tt.modern}
			}
			if tt.version != "" {
				obj.SetAPIVersion("cluster.x-k8s.io/" + tt.version)
				obj.Object["status"].(map[string]any)["deprecated"] = map[string]any{"v1beta1": map[string]any{"conditions": []any{condition("NodeHealthy", "False", "IgnoredDeprecatedFailure")}}}
			}
			before := obj.DeepCopy()
			typesBefore := append([]string(nil), tt.types...)
			got, found := capiMachineFalseCondition(obj, tt.types...)
			if found != (tt.wantType != "") || got.Type != tt.wantType || got.Reason != tt.wantReason {
				t.Fatalf("got %+v found=%v; want %s/%s", got, found, tt.wantType, tt.wantReason)
			}
			if !reflect.DeepEqual(obj, before) || !reflect.DeepEqual(tt.types, typesBefore) {
				t.Fatal("reader mutated its inputs")
			}
		})
	}
}
