package k8s

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestDynamicTerminatingProblems(t *testing.T) {
	defer ResetTestDynamicState()
	now := time.Now().UTC().Truncate(time.Second)
	widget := func(name, ns string, duration time.Duration) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("gaps.radar.test/v1")
		obj.SetKind("Widget")
		obj.SetName(name)
		obj.SetNamespace(ns)
		obj.SetUID(types.UID(name))
		obj.SetCreationTimestamp(metav1.NewTime(now.Add(-24 * time.Hour)))
		obj.SetDeletionTimestamp(&metav1.Time{Time: now.Add(-duration)})
		obj.SetFinalizers([]string{metav1.FinalizerDeleteDependents, "gaps.radar.test/cleanup", "elsewhere.test/cleanup"})
		return obj
	}
	namespaced := schema.GroupVersionResource{Group: "gaps.radar.test", Version: "v1", Resource: "widgets"}
	cluster := schema.GroupVersionResource{Group: "gaps.radar.test", Version: "v1", Resource: "clusterwidgets"}
	cold := schema.GroupVersionResource{Group: "gaps.radar.test", Version: "v1", Resource: "coldwidgets"}
	normal := widget("normal", "visible", 2*time.Hour)
	normal.SetDeletionTimestamp(nil)
	global := widget("global", "", 2*time.Hour)
	global.SetKind("ClusterWidget")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{namespaced: "WidgetList", cluster: "ClusterWidgetList", cold: "ColdWidgetList"}, widget("recent", "visible", 5*time.Minute), widget("warning", "visible", 15*time.Minute), widget("old", "visible", 2*time.Hour), widget("hidden", "hidden", 2*time.Hour), normal, global)
	if err := InitTestDynamicResourceCache(client, []APIResource{
		{Group: namespaced.Group, Version: "v1", Kind: "Widget", Name: namespaced.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
		{Group: cluster.Group, Version: "v1", Kind: "ClusterWidget", Name: cluster.Resource, IsCRD: true, Verbs: []string{"list", "watch"}},
		{Group: cold.Group, Version: "v1", Kind: "ColdWidget", Name: cold.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	dc := GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{namespaced, cluster} {
		if err := dc.EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
		if !dc.WaitForSync(gvr, 2*time.Second) {
			t.Fatal("not synced")
		}
	}
	scoped := DetectDynamicTerminatingProblems(dc, GetResourceDiscovery(), []string{"visible"}, now, nil)
	if len(scoped) != 2 {
		t.Fatalf("scoped=%+v", scoped)
	}
	for _, det := range scoped {
		if det.Namespace != "visible" || det.Group != namespaced.Group || det.Reason != "Terminating stuck" {
			t.Fatalf("bad scope/identity: %+v", det)
		}
		want := "critical"
		if det.Name == "warning" {
			want = "high"
		}
		if det.Severity != want {
			t.Fatalf("severity %s = %s, want %s", det.Name, det.Severity, want)
		}
		if det.OnsetUnknown || det.DurationSeconds < 900 || !strings.Contains(det.Cause, det.Duration) {
			t.Fatalf("bad deletion timing: %+v", det)
		}
		if !strings.Contains(det.Action, "kubectl patch 'widgets.gaps.radar.test'") || !strings.Contains(det.Action, "--dry-run=server") || !strings.Contains(det.Action, "external resources behind") || !strings.Contains(det.Action, "re-read") {
			t.Fatal(det.Action)
		}
		var input struct {
			Patch  string `json:"patch"`
			DryRun bool   `json:"dry_run"`
		}
		suffix := strings.SplitN(det.Action, "patch_resource ", 2)[1]
		if err := json.NewDecoder(strings.NewReader(suffix)).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if !input.DryRun {
			t.Fatal("expected preview")
		}
		patch, err := jsonpatch.DecodePatch([]byte(input.Patch))
		if err != nil {
			t.Fatal(err)
		}
		original := widget(det.Name, "visible", 2*time.Hour)
		data, _ := json.Marshal(original)
		result, err := patch.Apply(data)
		if err != nil {
			t.Fatal(err)
		}
		updated := &unstructured.Unstructured{}
		if err := json.Unmarshal(result, updated); err != nil {
			t.Fatal(err)
		}
		if got := updated.GetFinalizers(); len(got) != 2 || got[0] != metav1.FinalizerDeleteDependents || got[1] != "elsewhere.test/cleanup" {
			t.Fatalf("removed wrong finalizer: %v", got)
		}
		original.SetFinalizers([]string{"gaps.radar.test/cleanup", metav1.FinalizerDeleteDependents, "elsewhere.test/cleanup"})
		reordered, _ := json.Marshal(original)
		if _, err := patch.Apply(reordered); err == nil {
			t.Fatal("must reject reordered finalizers")
		}
		original.SetFinalizers([]string{metav1.FinalizerDeleteDependents, "gaps.radar.test/cleanup", "elsewhere.test/cleanup"})
		original.SetUID("replacement")
		replaced, _ := json.Marshal(original)
		if _, err := patch.Apply(replaced); err == nil {
			t.Fatal("must reject replacement object")
		}
	}
	all := DetectDynamicTerminatingProblems(dc, GetResourceDiscovery(), nil, now, nil)
	if len(all) != 4 {
		t.Fatalf("all=%+v", all)
	}
	calls := map[string]int{}
	filtered := DetectDynamicTerminatingProblems(dc, GetResourceDiscovery(), []string{"visible", "hidden"}, now, func(group, resource, namespace string) bool {
		if group != namespaced.Group || resource != namespaced.Resource {
			t.Fatalf("wrong GVR gate: %s/%s", group, resource)
		}
		calls[namespace]++
		return namespace == "visible"
	})
	if len(filtered) != 2 || calls["visible"] != 1 || calls["hidden"] != 1 {
		t.Fatalf("filtered=%+v calls=%v", filtered, calls)
	}
	if len(dc.WatchedGVRs()) != 2 {
		t.Fatalf("started cold watch: %v", dc.WatchedGVRs())
	}
	for _, obj := range mustListTerminatingWidgets(t, dc, namespaced) {
		if len(obj.GetFinalizers()) != 3 {
			t.Fatal("detector mutated cache")
		}
	}
}

func mustListTerminatingWidgets(t *testing.T, dc *DynamicResourceCache, gvr schema.GroupVersionResource) []*unstructured.Unstructured {
	t.Helper()
	items, err := dc.ListWatchedReadOnly(gvr)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDynamicTerminatingProtectionGuidance(t *testing.T) {
	t.Cleanup(ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	var objects []runtime.Object
	finalizers := []string{"gateway-exists-finalizer.gateway.networking.k8s.io", "snapshot.storage.kubernetes.io/volumesnapshot-bound-protection", "snapshot.storage.kubernetes.io/volumesnapshotcontent-bound-protection", "kubernetes.io/protection"}
	for i, finalizer := range finalizers {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion(gvr.Group + "/v1")
		u.SetKind("GatewayClass")
		u.SetName(string(rune('a' + i)))
		u.SetUID("guarded")
		u.SetDeletionTimestamp(&metav1.Time{Time: time.Now().Add(-time.Hour)})
		u.SetFinalizers([]string{finalizer})
		objects = append(objects, u)
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "GatewayClassList"}, objects...)
	if err := InitTestDynamicResourceCache(client, []APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "GatewayClass", Name: gvr.Resource, IsCRD: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	dc := GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("not synced")
	}
	out := DetectDynamicTerminatingProblems(dc, GetResourceDiscovery(), nil, time.Now(), nil)
	if len(out) != len(finalizers) {
		t.Fatalf("out=%+v", out)
	}
	for _, det := range out {
		if strings.Contains(det.Action, "patch_resource") || strings.Contains(det.Action, "kubectl patch") || !strings.Contains(det.Action, "in-use guard") || !strings.Contains(det.Action, "referencing") {
			t.Fatal(det.Action)
		}
	}
}
