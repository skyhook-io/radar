package k8s

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestInstalledAtReadsOwnDeployment(t *testing.T) {
	created := time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)
	kc := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: "radar", Namespace: "radar", CreationTimestamp: metav1.NewTime(created),
		}},
		// A same-named Deployment elsewhere must not be mistaken for ours.
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: "radar", Namespace: "other", CreationTimestamp: metav1.NewTime(created.Add(72 * time.Hour)),
		}},
	)
	if got := installedAtFrom(context.Background(), kc, "radar", "radar"); got != created.Unix() {
		t.Fatalf("installedAt = %d, want %d", got, created.Unix())
	}
}

// Every unknown must be 0 — callers treat that as "no identity" rather than
// falling back to something that would churn.
func TestInstalledAtZeroWhenUnknown(t *testing.T) {
	kc := fake.NewSimpleClientset()
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		client     kubernetes.Interface
		ns, deploy string
	}{
		{"no downward-API namespace", kc, "", "radar"},
		{"no downward-API name", kc, "radar", ""},
		{"deployment absent", kc, "radar", "radar"},
		{"nil client", nil, "radar", "radar"},
		// A typed nil in an interface is non-nil and panics on first use without the guard.
		{"typed-nil client", (*kubernetes.Clientset)(nil), "radar", "radar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := installedAtFrom(ctx, tc.client, tc.ns, tc.deploy); got != 0 {
				t.Fatalf("installedAt = %d, want 0", got)
			}
		})
	}
}
