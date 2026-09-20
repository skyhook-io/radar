package resourcecontext

// RayServiceSummary describes reported serving revisions, not a finite execution.
// Root Ready/upgrade/rollback/suspension conditions remain in StatusSummary.
// SubjectGeneration belongs to the RayService; ObservedGeneration is the native
// status attestation, not a guarantee that every nested field is current.
// SuspendRequested is intent (omitted spec.suspend means false in KubeRay).
// Suspension tears down owned resources and clears slots; once Suspending=True,
// teardown completes even if intent flips back. Resume creates new clusters.
type RayServiceSummary struct {
	SubjectGeneration  int64              `json:"subjectGeneration,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	SuspendRequested   bool               `json:"suspendRequested"`
	Active             *RayServiceRuntime `json:"active,omitempty"`
	Pending            *RayServiceRuntime `json:"pending,omitempty"`
}

// RayServiceRuntime uses only the root's named status slot. ClusterName is not
// an authorized navigable reference. Embedded RayCluster conditions are omitted:
// their changes alone do not trigger a RayService status update in KubeRay.
// Applications are name-sorted, capped at eight per slot, and keep native states;
// absence is unreported, never proof of health or zero applications. Full app
// messages and deployment states remain on the resource. These are reported
// snapshots, not end-to-end availability checks; deletion does not rewrite them.
// During NewCluster upgrades KubeRay stops refreshing the active application map
// and clears it; absence can coexist with a working active endpoint.
// Target capacity scales Serve replica targets; traffic is configured HTTPRoute
// weight, not measured requests. Nil means unreported (normal for non-incremental
// upgrades), not zero. An incremental upgrade may also not have reported yet.
type RayServiceRuntime struct {
	ClusterName           string                   `json:"clusterName"`
	TargetCapacityPercent *int64                   `json:"targetCapacityPercent,omitempty"`
	TrafficRoutedPercent  *int64                   `json:"trafficRoutedPercent,omitempty"`
	Applications          []ServeApplicationStatus `json:"applications,omitempty"`
	ApplicationsTruncated bool                     `json:"applicationsTruncated,omitempty"`
}

type ServeApplicationStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}
