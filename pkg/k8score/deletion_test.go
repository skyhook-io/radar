package k8score

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPendingDeletionPolicy(t *testing.T) {
	for _, tt := range []struct {
		name       string
		finalizers []string
		want       metav1.DeletionPropagation
		ok         bool
	}{
		{name: "nil"},
		{name: "empty", finalizers: []string{}},
		{name: "custom", finalizers: []string{"example.com/cleanup"}},
		{name: "foreground", finalizers: []string{metav1.FinalizerDeleteDependents}, want: metav1.DeletePropagationForeground, ok: true},
		{name: "orphan", finalizers: []string{metav1.FinalizerOrphanDependents}, want: metav1.DeletePropagationOrphan, ok: true},
		{name: "custom and foreground", finalizers: []string{"example.com/cleanup", metav1.FinalizerDeleteDependents}, want: metav1.DeletePropagationForeground, ok: true},
		{name: "custom and orphan", finalizers: []string{"example.com/cleanup", metav1.FinalizerOrphanDependents}, want: metav1.DeletePropagationOrphan, ok: true},
		{name: "both GC finalizers", finalizers: []string{metav1.FinalizerOrphanDependents, metav1.FinalizerDeleteDependents}, want: metav1.DeletePropagationForeground, ok: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := PendingDeletionPolicy(tt.finalizers)
			if got != tt.want || ok != tt.ok {
				t.Errorf("PendingDeletionPolicy(%v) = (%q, %v), want (%q, %v)", tt.finalizers, got, ok, tt.want, tt.ok)
			}
		})
	}
}
