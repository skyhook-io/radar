package schedulinginsight

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// KueueWorkloadAwaitingAdmission reports whether Kueue holds a Workload in
// its pending queues, so that asking its visibility API for the Workload's
// position can find it: no Finished, Admitted or QuotaReserved condition is
// True and spec.active is not false (Kueue drops deactivated Workloads from
// its queues). It reads any kueue.x-k8s.io Workload version, unlike
// ForResource. packages/k8s-ui mirrors it; both read
// testdata/kueue_awaiting_admission.json.
func KueueWorkloadAwaitingAdmission(obj runtime.Object) bool {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u.GroupVersionKind().Group != kueueWorkloadV1Beta2.Group || u.GetKind() != kueueWorkloadV1Beta2.Kind {
		return false
	}
	if active, found, _ := unstructured.NestedBool(u.Object, "spec", "active"); found && !active {
		return false
	}
	return workloadPhase(workloadConditions(u)) == resourcecontext.KueuePhasePending
}
