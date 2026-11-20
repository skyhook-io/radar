package resourcecontext

// ServingSummary groups controller-specific serving evidence without normalizing
// readiness, rollout state, or capacity across different serving models.
type ServingSummary struct {
	RayService             *RayServiceServing             `json:"rayService,omitempty"`
	KServeInferenceService *KServeInferenceServiceServing `json:"kserveInferenceService,omitempty"`
}

// RayServiceServing describes reported serving revisions, not a finite execution.
// Root Ready/upgrade/rollback/suspension conditions remain in StatusSummary.
// Compare ObservedGeneration with resource.metadata.generation; it is a native
// status attestation, not a guarantee that every nested field is current.
// SuspendRequested is intent (omitted spec.suspend means false in KubeRay).
// Suspension tears down owned resources and clears slots; once Suspending=True,
// teardown completes even if intent flips back. Resume creates new clusters.
type RayServiceServing struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	SuspendRequested   bool  `json:"suspendRequested"`
	// UpgradeStrategy is declared intent, not the effective operator default or rollout completion.
	UpgradeStrategy string             `json:"upgradeStrategy,omitempty"`
	Active          *RayServiceRuntime `json:"active,omitempty"`
	Pending         *RayServiceRuntime `json:"pending,omitempty"`
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

// KServeInferenceServiceServing contains root-only controller reports, not an
// availability check. Absent values are unreported, never healthy or zero.
// Conditions stay in StatusSummary; Ready can be True while TransformerReady is False.
// ModelMesh supplies only mode/runtime names because it has a separate writer.
type KServeInferenceServiceServing struct {
	DeploymentMode string `json:"deploymentMode,omitempty"`
	// Runtime names are reported selections, not proof a runtime was read or exists.
	ServingRuntimeName        string            `json:"servingRuntimeName,omitempty"`
	ClusterServingRuntimeName string            `json:"clusterServingRuntimeName,omitempty"`
	Model                     *KServeModelState `json:"model,omitempty"`
	Components                *KServeComponents `json:"components,omitempty"`
	// Canaries retain Standard-mode source order, capped at eight.
	Canaries          []KServeCanary `json:"canaries,omitempty"`
	CanariesTruncated bool           `json:"canariesTruncated,omitempty"`
	// ReferenceWriterRelease identifies the reference writer for FieldNotes, not the
	// installed controller version. Absent when there are no applicable notes.
	ReferenceWriterRelease string             `json:"referenceWriterRelease,omitempty"`
	FieldNotes             []ServingFieldNote `json:"fieldNotes,omitempty"`
}

// ServingFieldNote documents one raw Kubernetes field's writer semantics with
// fixed adapter text, never a verdict composed from resource values. Field is
// a dotted path in the Kubernetes object; [] means any array entry. Detail
// retains these fields; summary/compact minification may omit them. Absence of
// a note means no interpretation was supplied, not that the field is reliable.
type ServingFieldNote struct {
	Field string `json:"field"`
	Note  string `json:"note"`
}

// KServeModelState covers the main predictor only. In Standard mode its writer
// uses the newest Pod or Deployment availability as a fallback, so UpToDate and
// Loaded can precede a new rollout Pod starting. The active state and copies
// are deliberately omitted; see FieldNotes for their raw-field semantics.
type KServeModelState struct {
	// TransitionStatus preserves reported empty strings as distinct from absence.
	TransitionStatus *string `json:"transitionStatus,omitempty"`
	// TargetModelState is reported state of the target predictor model, not a
	// desired state. Empty or unreported values are omitted.
	TargetModelState string `json:"targetModelState,omitempty"`
	// LastFailure can outlive recovery or a later attempt; stopping clears status.
	LastFailure *KServeModelFailure `json:"lastFailure,omitempty"`
}

// KServeModelFailure omits the raw message. The writer omits a zero exit code,
// so absent ExitCode means zero or unreported.
type KServeModelFailure struct {
	Reason   string `json:"reason,omitempty"`
	ExitCode *int64 `json:"exitCode,omitempty"`
}

// KServeComponents has fixed Knative revision/traffic slots; Standard reports
// no such facts. Absent slots are unreported, not evidence of no component.
type KServeComponents struct {
	Predictor   *KServeComponent `json:"predictor,omitempty"`
	Transformer *KServeComponent `json:"transformer,omitempty"`
	Explainer   *KServeComponent `json:"explainer,omitempty"`
}

// KServeComponent retains native revision names and configured traffic in source
// order, capped at eight. Weights are not measured requests. LatestRevision
// targets the latest ready revision, which can differ from the latest created.
type KServeComponent struct {
	LatestReadyRevision   string `json:"latestReadyRevision,omitempty"`
	LatestCreatedRevision string `json:"latestCreatedRevision,omitempty"`
	// Rolledout revisions track full-rollout history (including rollback
	// adjustment), not the current split. Read Traffic for configured weights.
	PreviousRolledoutRevision string                  `json:"previousRolledoutRevision,omitempty"`
	LatestRolledoutRevision   string                  `json:"latestRolledoutRevision,omitempty"`
	Traffic                   []KServeRevisionTraffic `json:"traffic,omitempty"`
	TrafficTruncated          bool                    `json:"trafficTruncated,omitempty"`
}

// KServeRevisionTraffic preserves absent, false and zero as different reports.
type KServeRevisionTraffic struct {
	RevisionName   string `json:"revisionName,omitempty"`
	LatestRevision *bool  `json:"latestRevision,omitempty"`
	Percent        *int64 `json:"percent,omitempty"`
	Tag            string `json:"tag,omitempty"`
}

// KServeCanary is a Standard-mode report: Ready means an available Deployment
// replica and TrafficPercent is declared intent, not measured traffic. Absent
// readiness/weight is unreported. Its model status is never filled by this writer.
type KServeCanary struct {
	// Name identifies spec.canary[].predictor.name, not a Deployment reference.
	Name           string `json:"name"`
	Ready          *bool  `json:"ready,omitempty"`
	TrafficPercent *int64 `json:"trafficPercent,omitempty"`
}
