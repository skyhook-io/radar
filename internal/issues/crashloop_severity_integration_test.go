package issues

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestCompose_CrashLoopCurrentStateControlsRankingAndGrouping(t *testing.T) {
	defer k8s.ResetTestState()

	now := time.Now()
	controller := true
	crashState := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason:     "Error",
		ExitCode:   1,
		FinishedAt: metav1.NewTime(now.Add(-time.Minute)),
	}}
	recovered := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "prod",
				CreationTimestamp: metav1.NewTime(now.Add(-8 * time.Minute)),
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "catalog-rs",
					Controller: &controller,
				}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:                 "app",
					Ready:                true,
					RestartCount:         2,
					State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Minute))}},
					LastTerminationState: crashState,
				}},
			},
		}
	}
	down := recovered("down-now")
	down.OwnerReferences = nil
	down.Status.ContainerStatuses[0].Ready = false
	down.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
	}

	replicas := int32(2)
	client := fake.NewClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "prod"},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
			Status: appsv1.DeploymentStatus{
				Replicas:          2,
				ReadyReplicas:     2,
				AvailableReplicas: 2,
				UpdatedReplicas:   2,
			},
		},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name:      "catalog-rs",
			Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "catalog",
				Controller: &controller,
			}},
		}},
		recovered("catalog-a"),
		recovered("catalog-b"),
		down,
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	provider := &CacheProvider{cache: k8s.GetResourceCache()}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(provider.DetectProblems([]string{"prod"})) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got := Compose(provider, Filters{Namespaces: []string{"prod"}, Grouped: true, Limit: NoLimit})
	if len(got) < 2 {
		t.Fatalf("issues = %+v, want critical down row and grouped warning row", got)
	}
	if got[0].Name != "down-now" || got[0].Severity != SeverityCritical {
		t.Fatalf("first issue = %+v, want currently-down crashloop ranked critical first", got[0])
	}

	var grouped *Issue
	for i := range got {
		if got[i].Category == issuesapi.CategoryCrashLoop && got[i].Name == "catalog" {
			grouped = &got[i]
			break
		}
	}
	if grouped == nil {
		t.Fatalf("issues = %+v, want catalog crashloop group", got)
	}
	if grouped.Severity != SeverityWarning || grouped.Count != 2 {
		t.Fatalf("catalog group = %+v, want warning with two affected pods", *grouped)
	}
	if !strings.Contains(grouped.Cause, "is serving again") || strings.Contains(grouped.Cause, "is crashlooping") {
		t.Fatalf("catalog group cause = %q, want serving recovery copy", grouped.Cause)
	}

	related := RelatedIssues(provider, []string{"prod"}, "", "Pod", "prod", "catalog-a")
	if len(related) != 1 || related[0].Severity != SeverityWarning {
		t.Fatalf("related issues for recovered pod = %+v, want its warning crashloop", related)
	}
}
