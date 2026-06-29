package k8s

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func capiObj(group, kind, name string, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v1beta1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": "capi-system", "generation": int64(1)},
		"status":     status,
	}}
}

func TestDetectCAPIProblems_CuratedConditionCoverage(t *testing.T) {
	defer ResetTestDynamicState()

	const capiGroup = "cluster.x-k8s.io"
	machineGVR := schema.GroupVersionResource{Group: capiGroup, Version: "v1beta1", Resource: "machines"}
	mdGVR := schema.GroupVersionResource{Group: capiGroup, Version: "v1beta1", Resource: "machinedeployments"}
	mhcGVR := schema.GroupVersionResource{Group: capiGroup, Version: "v1beta1", Resource: "machinehealthchecks"}

	readyFalse := func(reason string) []any {
		return []any{map[string]any{"type": "Ready", "status": "False", "reason": reason, "message": reason + " detail"}}
	}

	objs := []runtime.Object{
		capiObj(capiGroup, "Machine", "machine-ready", map[string]any{
			"phase":              "Running",
			"observedGeneration": int64(1),
			"conditions":         readyFalse("ReadyFailed"),
		}),
		capiObj(capiGroup, "MachineDeployment", "md-ready", map[string]any{
			"readyReplicas":      int64(3),
			"observedGeneration": int64(1),
			"conditions":         readyFalse("RolloutFailed"),
		}),
		capiObj(capiGroup, "MachineHealthCheck", "mhc-ready", map[string]any{
			"expectedMachines":   int64(3),
			"currentHealthy":     int64(3),
			"observedGeneration": int64(1),
			"conditions":         readyFalse("RemediationBlocked"),
		}),
		capiObj(capiGroup, "MachineDeployment", "md-stale", map[string]any{
			"readyReplicas":      int64(3),
			"observedGeneration": int64(0),
			"conditions":         readyFalse("Progressing"),
		}),
	}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			machineGVR: "MachineList",
			mdGVR:      "MachineDeploymentList",
			mhcGVR:     "MachineHealthCheckList",
		},
		objs...,
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: capiGroup, Version: "v1beta1", Kind: "Machine", Name: "machines", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Group: capiGroup, Version: "v1beta1", Kind: "MachineDeployment", Name: "machinedeployments", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Group: capiGroup, Version: "v1beta1", Kind: "MachineHealthCheck", Name: "machinehealthchecks", Namespaced: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := GetDynamicResourceCache()
	discovery := GetResourceDiscovery()
	for _, gvr := range []schema.GroupVersionResource{machineGVR, mdGVR, mhcGVR} {
		if err := dynCache.EnsureWatching(gvr); err != nil {
			t.Fatalf("EnsureWatching %s: %v", gvr, err)
		}
		if !dynCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("dynamic cache for %s did not sync", gvr)
		}
	}

	problems := DetectCAPIProblems(dynCache, discovery, "")
	byName := map[string]Detection{}
	for _, p := range problems {
		byName[p.Name] = p
	}
	for name, reason := range map[string]string{
		"machine-ready": "ReadyFailed",
		"md-ready":      "RolloutFailed",
		"mhc-ready":     "RemediationBlocked",
	} {
		p, ok := byName[name]
		if !ok {
			t.Fatalf("%s Ready=False condition disappeared from curated CAPI detector; got %+v", name, problems)
		}
		if p.Reason != reason || p.Severity != "high" {
			t.Fatalf("%s = reason/severity %q/%q, want %q/high", name, p.Reason, p.Severity, reason)
		}
	}
	if p, ok := byName["md-stale"]; ok {
		t.Fatalf("transient MachineDeployment condition should not be flagged: %+v", p)
	}
}
