// Package servinginsight projects bounded controller-reported serving facts.
// It does not fetch children or infer end-to-end availability.
package servinginsight

import (
	"sort"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var rayServiceV1 = schema.GroupVersionKind{Group: "ray.io", Version: "v1", Kind: "RayService"}

const maxApplications = 8

// ForResource returns serving evidence only for exact, supported resource identities.
func ForResource(obj runtime.Object) *resourcecontext.ServingSummary {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u.GroupVersionKind() != rayServiceV1 {
		return nil
	}
	return &resourcecontext.ServingSummary{RayService: forRayService(u)}
}

// forRayService follows KubeRay v1.7.0; the served API version alone does not
// identify the controller release. Ready means proxy endpoints exist, not that
// all Serve applications or revisions are healthy. Independent root conditions
// already live in statusSummary; no combined phase or outcome is synthesized.
func forRayService(u *unstructured.Unstructured) *resourcecontext.RayServiceServing {
	suspendRequested, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	observedGeneration, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	upgradeStrategy, _, _ := unstructured.NestedString(u.Object, "spec", "upgradeStrategy", "type")
	return &resourcecontext.RayServiceServing{
		ObservedGeneration: observedGeneration,
		SuspendRequested:   suspendRequested,
		UpgradeStrategy:    upgradeStrategy,
		Active:             rayServiceRuntime(u, "activeServiceStatus"),
		Pending:            rayServiceRuntime(u, "pendingServiceStatus"),
	}
}

func rayServiceRuntime(u *unstructured.Unstructured, field string) *resourcecontext.RayServiceRuntime {
	status, found, _ := unstructured.NestedMap(u.Object, "status", field)
	if !found {
		return nil
	}
	name, _, _ := unstructured.NestedString(status, "rayClusterName")
	if name == "" {
		return nil
	}
	runtime := &resourcecontext.RayServiceRuntime{
		ClusterName:           name,
		TargetCapacityPercent: int64PointerAt(status, "targetCapacity"),
		TrafficRoutedPercent:  int64PointerAt(status, "trafficRoutedPercent"),
	}
	apps, _, _ := unstructured.NestedMap(status, "applicationStatuses")
	names := make([]string, 0, len(apps))
	for name := range apps {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > maxApplications {
		names = names[:maxApplications]
		runtime.ApplicationsTruncated = true
	}
	for _, name := range names {
		state, _, _ := unstructured.NestedString(apps, name, "status")
		runtime.Applications = append(runtime.Applications, resourcecontext.ServeApplicationStatus{Name: name, Status: state})
	}
	return runtime
}

func int64PointerAt(object map[string]any, fields ...string) *int64 {
	value, found, _ := unstructured.NestedInt64(object, fields...)
	if !found {
		return nil
	}
	return &value
}
