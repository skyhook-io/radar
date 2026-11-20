package context

// OpenTelemetryCollectorSummary preserves the operator's native declarations
// and reported Pod snapshot, whose denominator differs from workload Ready.
type OpenTelemetryCollectorSummary struct {
	// ReportedPodReadiness copies status.scale.statusReplicas: ready/observed
	// Pods for Deployment/StatefulSet, ready/desired-scheduled for DaemonSet.
	// Failed reads or a transition to sidecar mode can retain an earlier value;
	// this is not a current runtime readiness check.
	ReportedPodReadiness string `json:"reportedPodReadiness,omitempty"`
	// DeclaredPipelines is absent when config is unavailable, malformed or over
	// the parsing budget. An available configuration with none has Count=0.
	DeclaredPipelines *NamePreview `json:"declaredPipelines,omitempty"`
}

// NamePreview has a known full count and a bounded, sorted name preview.
// It is not an exact membership set: names may be omitted or shortened.
type NamePreview struct {
	Count int      `json:"count"`
	Names []string `json:"names,omitempty"`
	// NamesTruncated means names were omitted or a name was shortened.
	NamesTruncated bool `json:"namesTruncated,omitempty"`
}

// FlaggerCanarySummary contains Flagger's recorded counters. No observedGeneration
// attests these to the latest spec, and weight is not a traffic measurement.
// Missing counters are unreported; pointers preserve a reported zero.
type FlaggerCanarySummary struct {
	ReportedWeightPercent *int64 `json:"reportedWeightPercent,omitempty"`
	ReportedFailedChecks  *int64 `json:"reportedFailedChecks,omitempty"`
	ReportedIterations    *int64 `json:"reportedIterations,omitempty"`
}

// OpenTelemetryInstrumentationSummary reports the operator's recorded upgrade
// blocks, not application health or proof a current image remains blocked.
type OpenTelemetryInstrumentationSummary struct {
	// RecordedUpgradeBlocks previews language keys in status.upgradeBlockedVersions;
	// arbitrary status messages and instrumentation settings stay in detail reads.
	RecordedUpgradeBlocks NamePreview `json:"recordedUpgradeBlocks"`
}
