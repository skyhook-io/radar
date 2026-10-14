package server

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// With the timeline down, /api/issues must say its recent changes could not
// be read. A missing recent_changes alone reads the same as none.
func TestIssuesSayWhenRecentChangesCouldNotBeRead(t *testing.T) {
	// The package fixture's broken Deployment has no creation time, so the
	// feed is never due for it; this one was created ten minutes ago.
	created := time.Now().Add(-10 * time.Minute)
	replicas := int32(1)
	k8s.ResetResourceCache()
	t.Cleanup(func() {
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
	if err := k8s.InitTestResourceCache(fake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop", CreationTimestamp: metav1.NewTime(created)},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "api:v1"}}},
			},
		},
		Status: appsv1.DeploymentStatus{
			UnavailableReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(created.Add(5 * time.Second)),
			}},
		},
	})); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	timeline.ResetStore()
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	var response issuesapi.Response
	assertOK(t, get(t, "/api/issues?namespace=shop"), &response)
	if len(response.Issues) == 0 {
		t.Fatal("setup: no issues, so no recent-changes feed was due")
	}
	if response.RecentChangesError == "" {
		t.Fatalf("recent_changes_error is empty with no timeline store: %+v", response)
	}
}
