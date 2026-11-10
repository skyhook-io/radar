package k8score

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PendingDeletionPolicy reports only policies recorded by GC finalizers;
// Background has no finalizer and must be requested explicitly.
func PendingDeletionPolicy(finalizers []string) (metav1.DeletionPropagation, bool) {
	switch {
	case slices.Contains(finalizers, metav1.FinalizerDeleteDependents):
		return metav1.DeletePropagationForeground, true
	case slices.Contains(finalizers, metav1.FinalizerOrphanDependents):
		return metav1.DeletePropagationOrphan, true
	default:
		return "", false
	}
}
