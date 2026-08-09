package k8score

import policyv1 "k8s.io/api/policy/v1"

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
