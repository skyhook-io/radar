package k8s

// Detector-facing reason tokens. These name the problem categories the workload
// detector emits and compares against the canonical reasons pkg/health produces
// (e.g. CrashLoopBackOff, HighRestartCount, ReadinessProbeFailed). They live here
// rather than in pkg/health because they are detector vocabulary, not part of the
// shared health classifier.
const (
	crashLoopReason            = "CrashLoopBackOff"
	highRestartReason          = "HighRestartCount"
	readinessProbeFailedReason = "ReadinessProbeFailed"
	readinessProbeInvalidReason = "ReadinessProbeInvalid"
	livenessProbeInvalidReason  = "LivenessProbeInvalid"
	initContainerStalledReason  = "InitContainerStalled"
)

// highRestartThreshold is the cumulative per-container RestartCount above which a
// still-unhealthy container is treated as actively thrashing.
const highRestartThreshold = 3
