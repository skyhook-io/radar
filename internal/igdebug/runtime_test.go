package igdebug

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	domain "github.com/skyhook-io/radar/pkg/igdebug"
)

func TestResolveTargetDefaultsToFirstRunningContainer(t *testing.T) {
	pod := targetPod("old-uid", "containerd://first-id", "containerd://second-id")
	gadget := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gadget-node-a", Namespace: "gadget", Labels: map[string]string{"k8s-app": "gadget"}},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	client := fake.NewSimpleClientset(pod, gadget)

	target, err := resolveTarget(context.Background(), client, "gadget", domain.Target{Namespace: "app", Pod: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Container != "first" || target.ContainerID != "first-id" || target.PodUID != "old-uid" || target.Node != "node-a" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolveTargetHonorsDefaultContainerAnnotation(t *testing.T) {
	pod := targetPod("old-uid", "containerd://proxy-id", "containerd://app-id")
	pod.Annotations = map[string]string{"kubectl.kubernetes.io/default-container": "second"}
	gadget := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gadget-node-a", Namespace: "gadget", Labels: map[string]string{"k8s-app": "gadget"}},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	target, err := resolveTarget(context.Background(), fake.NewSimpleClientset(pod, gadget), "gadget", domain.Target{Namespace: "app", Pod: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Container != "second" || target.ContainerID != "app-id" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolveTargetRejectsTerminatedContainerWithRetainedID(t *testing.T) {
	pod := targetPod("old-uid", "containerd://first-id", "")
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
	client := fake.NewSimpleClientset(pod)

	_, err := resolveTarget(context.Background(), client, "gadget", domain.Target{Namespace: "app", Pod: "api", Container: "first"})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("error = %v", err)
	}
}

func TestWatchTargetMarksRecreatedPodPartial(t *testing.T) {
	pod := targetPod("new-uid", "containerd://new-id", "")
	client := fake.NewSimpleClientset(pod)
	reporter := &captureReporter{partial: make(chan string, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchTarget(ctx, client, domain.Target{Namespace: "app", Pod: "api", PodUID: "old-uid", Node: "node-a", Container: "first", ContainerID: "old-id"}, reporter)

	select {
	case reason := <-reporter.partial:
		if !strings.Contains(reason, "replaced") {
			t.Fatalf("reason = %q", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("target replacement was not detected")
	}
}

func TestWatchTargetToleratesTransientGetFailures(t *testing.T) {
	pod := targetPod("old-uid", "containerd://old-id", "")
	client := fake.NewSimpleClientset(pod)
	attempts := 0
	client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		attempts++
		if attempts < 3 {
			return true, nil, errors.New("temporary apiserver failure")
		}
		return false, nil, nil
	})
	reporter := &captureReporter{partial: make(chan string, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	go watchTargetEvery(ctx, client, domain.Target{Namespace: "app", Pod: "api", PodUID: "old-uid", Node: "node-a", Container: "first", ContainerID: "old-id"}, reporter, time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case reason := <-reporter.partial:
		t.Fatalf("transient failures ended capture: %s", reason)
	default:
	}
	if attempts < 3 {
		t.Fatalf("attempts = %d, want at least 3", attempts)
	}
}

func TestVisibleEnricherStripsResourcesOutsideVisibleSet(t *testing.T) {
	enricher := &visibleEnricher{
		services: []corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "app"}, Spec: corev1.ServiceSpec{ClusterIPs: []string{"10.0.0.10"}}}},
		pods:     []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "app"}, Status: corev1.PodStatus{PodIP: "10.0.0.2"}}},
	}
	rows := []domain.Connection{{
		Local:  domain.Endpoint{Address: "10.0.0.2", Resource: &domain.EndpointRef{Kind: "pod", Namespace: "other", Name: "secret"}},
		Remote: domain.Endpoint{Address: "10.0.0.10"},
	}}
	got := enricher.connections(rows)
	if got[0].Local.Resource == nil || got[0].Local.Resource.Namespace != "app" || got[0].Local.Resource.Name != "api" {
		t.Fatalf("local resource = %#v", got[0].Local.Resource)
	}
	if got[0].Remote.Resource == nil || got[0].Remote.Resource.Name != "db" {
		t.Fatalf("remote resource = %#v", got[0].Remote.Resource)
	}
}

func TestSnapshotDeadlineReportsPartialAfterStart(t *testing.T) {
	started := make(chan struct{})
	completed := make(chan struct{})
	reporter := &captureReporter{partial: make(chan string, 1)}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go enforceSnapshotDeadline(ctx, started, completed, time.Millisecond, reporter, cancel)
	close(started)
	select {
	case reason := <-reporter.partial:
		if !strings.Contains(reason, "timed out") {
			t.Fatalf("reason = %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot deadline did not fire")
	}
}

func targetPod(uid, firstID, secondID string) *corev1.Pod {
	statuses := []corev1.ContainerStatus{{
		Name: "first", ContainerID: firstID,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	if secondID != "" {
		statuses = append(statuses, corev1.ContainerStatus{
			Name: "second", ContainerID: secondID,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "app", UID: types.UID(uid)},
		Spec: corev1.PodSpec{
			NodeName: "node-a",
			Containers: []corev1.Container{
				{Name: "first"},
				{Name: "second"},
			},
		},
		Status: corev1.PodStatus{ContainerStatuses: statuses},
	}
}

type captureReporter struct {
	partial chan string
}

func (r *captureReporter) Started(domain.Target) {}
func (r *captureReporter) Event(json.RawMessage) {}
func (r *captureReporter) Result(domain.Result)  {}
func (r *captureReporter) Partial(reason string) { r.partial <- reason }
