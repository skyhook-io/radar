package context

// CollectorSummary keeps the operator's reported Pod snapshot separate from
// ResourceSummary.Ready, which would imply current observed readiness.
type CollectorSummary struct {
	// ReportedPodReadiness is status.scale.statusReplicas at the last reconcile:
	// ready/observed Pods for Deployment/StatefulSet, ready/scheduled for DaemonSet.
	// It does not attest current runtime readiness.
	ReportedPodReadiness string `json:"reportedPodReadiness,omitempty"`
	// DeclaredPipelines is absent when config is unavailable, malformed or over
	// the preview budget. An available configuration with none has Count=0.
	DeclaredPipelines *PipelineSummary `json:"declaredPipelines,omitempty"`
}

type PipelineSummary struct {
	// Count includes all declared pipeline names before preview limits.
	Count int      `json:"count"`
	Names []string `json:"names,omitempty"`
	// NamesTruncated means names were omitted or a name was shortened.
	NamesTruncated bool `json:"namesTruncated,omitempty"`
}

// CanarySummary contains Flagger's recorded counters. No observedGeneration
// attests these to the latest spec, and weight is not a traffic measurement.
// Missing counters are unreported; pointers preserve a reported zero.
type CanarySummary struct {
	ReportedWeightPercent *int64 `json:"reportedWeightPercent,omitempty"`
	ReportedFailedChecks  *int64 `json:"reportedFailedChecks,omitempty"`
	ReportedIterations    *int64 `json:"reportedIterations,omitempty"`
}

// InstrumentationSummary names the languages with recorded upgrade blocks.
// Arbitrary status messages and instrumentation settings stay in detail reads.
type InstrumentationSummary struct {
	BlockedUpgradeLanguages     []string `json:"blockedUpgradeLanguages"`
	BlockedUpgradeLanguageCount int      `json:"blockedUpgradeLanguageCount"`
	// BlockedUpgradeLanguagesTruncated means names were omitted or shortened.
	BlockedUpgradeLanguagesTruncated bool `json:"blockedUpgradeLanguagesTruncated,omitempty"`
}
