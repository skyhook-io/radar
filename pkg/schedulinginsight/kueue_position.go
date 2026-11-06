package schedulinginsight

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// KueueWorkloadEligibleForPendingPosition reports whether the supplied
// Workload snapshot permits a visibility lookup: no Finished, Admitted or
// QuotaReserved condition is True and spec.active is not false. True does
// not establish that Kueue has observed or queued it. It reads any
// kueue.x-k8s.io Workload version, unlike ForResource. The UI mirrors it;
// both read testdata/kueue_awaiting_admission.json.
func KueueWorkloadEligibleForPendingPosition(obj runtime.Object) bool {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u.GroupVersionKind().Group != "kueue.x-k8s.io" || u.GetKind() != "Workload" {
		return false
	}
	if active, found, _ := unstructured.NestedBool(u.Object, "spec", "active"); found && !active {
		return false
	}
	return workloadPhase(workloadConditions(u)) == resourcecontext.KueuePhasePending
}
