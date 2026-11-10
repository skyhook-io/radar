package mcp

import (
	"context"
	"encoding/json"
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

// installUnavailableDeployment installs a Deployment with no available replicas,
// enough for both the issues feed and the dashboard to look for recent changes.
func installUnavailableDeployment(t *testing.T) {
	t.Helper()
	created := time.Now().Add(-10 * time.Minute)
	replicas := int32(1)
	client := fake.NewSimpleClientset(&appsv1.Deployment{
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
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
}

// With the timeline down, the issues feed must say its recent changes could
// not be read. A missing recent_changes alone reads the same as none.
func TestIssuesToolSaysWhenRecentChangesCouldNotBeRead(t *testing.T) {
	installUnavailableDeployment(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)

	result, _, err := handleIssuesTool(context.Background(), nil, issuesInput{Namespace: "shop"})
	if err != nil {
		t.Fatalf("handleIssuesTool: %v", err)
	}
	var response issuesapi.Response
	if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Issues) == 0 || response.RecentChangesReason != "" {
		t.Fatalf("setup: want issues and no feed, got %d issue(s), reason %q", len(response.Issues), response.RecentChangesReason)
	}
	if response.RecentChangesError == "" {
		t.Fatal("recent_changes_error is empty with no timeline store; the missing feed reads as no changes")
	}
}

func TestDashboardSaysWhenRecentChangesCouldNotBeRead(t *testing.T) {
	installUnavailableDeployment(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)

	var dashboard mcpDashboard
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dashboard = buildDashboard(context.Background(), k8s.GetResourceCache(), "shop", false, false)
		if len(dashboard.Problems) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(dashboard.Problems) == 0 {
		t.Fatal("setup: the dashboard found no problems to look up changes for")
	}
	if dashboard.RecentChangesError == "" {
		t.Fatal("recentChangesError is empty with no timeline store; the missing changes read as none")
	}
}
