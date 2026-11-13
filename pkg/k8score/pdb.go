package k8score

import policyv1 "k8s.io/api/policy/v1"

// PDBBlockedEvictionAction also applies when the budget's manager is unknown.
// A missing controller inventory must not become advice to bypass its policy.
const PDBBlockedEvictionAction = "For a controller-managed budget, follow the controller's drain policy and check its health instead of relaxing the budget manually. Otherwise, add replicas or review minAvailable/maxUnavailable with the workload owner before draining."

// PDBEvictionState distinguishes authoritative drain blockers from stale
// controller status. Callers must not turn stale status into a green result.
type PDBEvictionState string

const (
	PDBEvictionAllowed PDBEvictionState = "allowed"
	PDBEvictionBlocked PDBEvictionState = "blocked"
	PDBEvictionUnknown PDBEvictionState = "unknown"
)

func PodDisruptionBudgetEvictionState(pdb *policyv1.PodDisruptionBudget) PDBEvictionState {
	if pdb == nil || pdb.DeletionTimestamp != nil {
		return PDBEvictionAllowed
	}
	if pdb.Status.ObservedGeneration != pdb.Generation {
		return PDBEvictionUnknown
	}
	if pdb.Status.ExpectedPods > 0 &&
		pdb.Status.DisruptionsAllowed == 0 &&
		pdb.Status.CurrentHealthy >= pdb.Status.ExpectedPods &&
		pdb.Status.DesiredHealthy >= pdb.Status.ExpectedPods {
		return PDBEvictionBlocked
	}
	return PDBEvictionAllowed
}
