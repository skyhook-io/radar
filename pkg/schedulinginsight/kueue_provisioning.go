package schedulinginsight

import (
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ProvisioningRequestGroup is the API group of cluster-autoscaler's
// ProvisioningRequest, which Kueue's provisioning AdmissionCheck creates.
const ProvisioningRequestGroup = "autoscaling.x-k8s.io"

// KueueProvisioningRequests returns the ProvisioningRequests among items whose
// controller owner is this incarnation of the Workload (group, kind, name and
// UID), newest first. Kueue may delete a request after its attempt ends, so
// the result is what is retained, not every attempt. A request is not matched
// to a particular AdmissionCheck: its name is not a contract.
func KueueProvisioningRequests(workload *unstructured.Unstructured, items []*unstructured.Unstructured) []*unstructured.Unstructured {
	if workload == nil || workload.GetUID() == "" {
		return nil
	}
	var matches []*unstructured.Unstructured
	for _, item := range items {
		if item == nil || item.GroupVersionKind().Group != ProvisioningRequestGroup || item.GetKind() != "ProvisioningRequest" || item.GetNamespace() != workload.GetNamespace() {
			continue
		}
		owner := metav1.GetControllerOf(item)
		if owner == nil || resourceid.GroupFromAPIVersion(owner.APIVersion) != kueueGroup || owner.Kind != kueueWorkloadKind || owner.Name != workload.GetName() || owner.UID != workload.GetUID() {
			continue
		}
		matches = append(matches, item)
	}
	sort.Slice(matches, func(i, j int) bool {
		at, bt := matches[i].GetCreationTimestamp(), matches[j].GetCreationTimestamp()
		if !at.Equal(&bt) {
			return at.After(bt.Time)
		}
		if matches[i].GetName() != matches[j].GetName() {
			return matches[i].GetName() < matches[j].GetName()
		}
		return matches[i].GetUID() < matches[j].GetUID()
	})
	return matches
}
