package health

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/conditions"
)

// OpenTelemetryCollector classifies operator reconciliation, not the health of
// its Pods or delivery of telemetry. The operator's Ready=True means only that
// reconciliation succeeded; status.scale is a separate reconciliation snapshot.
func OpenTelemetryCollector(u *unstructured.Unstructured) Verdict {
	if u == nil {
		return Verdict{Level: LevelUnknown, Reason: "Not reported"}
	}
	management, _, _ := unstructured.NestedString(u.Object, "spec", "managementState")
	if management == "unmanaged" {
		return Verdict{Level: LevelNeutral, Reason: "Unmanaged"}
	}
	ready, found := conditions.Find(u, "Ready")
	if !found {
		return Verdict{Level: LevelUnknown, Reason: "Not reported"}
	}
	observed, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if observed > 0 && observed < u.GetGeneration() {
		return Verdict{Level: LevelUnknown, Reason: "Status stale"}
	}
	switch ready.Status {
	case "False":
		if ready.Reason == "StatusUpdateFailed" {
			return Verdict{Level: LevelUnknown, Reason: "Status unavailable"}
		}
		return Verdict{Level: LevelUnhealthy, Reason: "Reconcile failed", Message: ready.Reason}
	case "True":
		return Verdict{Level: LevelNeutral, Reason: "Reconciled"}
	default:
		return Verdict{Level: LevelUnknown, Reason: "Reconciliation unknown"}
	}
}

// OpenTelemetryInstrumentation describes configuration/upgrade state. An
// Instrumentation object does not report readiness or successful injection.
func OpenTelemetryInstrumentation(u *unstructured.Unstructured) Verdict {
	if u == nil {
		return Verdict{Level: LevelUnknown, Reason: "Not reported"}
	}
	blocked, _, _ := unstructured.NestedMap(u.Object, "status", "upgradeBlockedVersions")
	if len(blocked) > 0 {
		return Verdict{Level: LevelNeutral, Reason: "Upgrade blocked"}
	}
	return Verdict{Level: LevelNeutral, Reason: "Configured"}
}
