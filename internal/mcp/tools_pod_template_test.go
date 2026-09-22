package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetResourcePodTemplateContextIsOptIn(t *testing.T) {
	controller := true
	template := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "pod", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: "owner", UID: "owner", Controller: &controller}}}, Spec: *template.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: 5, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(time.Now())}}}}}}
	pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("64Mi")
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "test", UID: "owner"}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: template}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod, rs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8s.ResetTestState(); getPermCache().Invalidate() })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	ctx := withClusterAdmin(t, "template-context")
	getPermCache().Get("template-context", nil).SetCanI("get", "apps", "replicasets", "test", true)
	for _, tc := range []struct {
		name, include, context string
		want                   bool
	}{{"default", "", "", false}, {"explicit", "issues", "", true}, {"without context", "issues", "none", true}, {"bare", "", "none", false}} {
		t.Run(tc.name, func(t *testing.T) {
			result, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "pod", Namespace: "test", Name: "worker", Include: tc.include, Context: tc.context})
			if err != nil {
				t.Fatal(err)
			}
			text := extractText(t, result)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(text), &fields); err != nil {
				t.Fatal(err)
			}
			if _, invalid := fields["includeError"]; invalid {
				t.Fatalf("include rejected: %s", text)
			}
			_, present := fields["relatedIssues"]
			if present != tc.want {
				t.Fatalf("relatedIssues present=%v response=%s", present, text)
			}
			if tc.want && !strings.Contains(string(fields["relatedIssues"]), "pod_template_divergence") {
				t.Fatalf("missing difference: %s", text)
			}
			if !tc.want && strings.Contains(text, "pod_template_divergence") {
				t.Fatalf("default context leaked detailed fact: %s", text)
			}
			if tc.context == "none" {
				if _, present := fields["resourceContext"]; present {
					t.Fatalf("context none included resourceContext: %s", text)
				}
			}
		})
	}
	getPermCache().Get("template-context", nil).SetCanI("get", "apps", "replicasets", "test", false)
	result, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "pod", Namespace: "test", Name: "worker", Include: "issues"})
	if err != nil {
		t.Fatal(err)
	}
	if text := extractText(t, result); strings.Contains(text, "pod_template_divergence") || strings.Contains(text, "128Mi") {
		t.Fatalf("unreadable owner evidence leaked: %s", text)
	}
}

func TestRelatedIssueDetailsBounded(t *testing.T) {
	for _, n := range []int{0, 1, 3, 4} {
		rows := make([]issues.Issue, n)
		result := map[string]any{}
		attachRelatedIssueDetails(result, rows)
		if result["relatedIssuesTotal"] != n || result["relatedIssuesTruncated"] != (n > 3) {
			t.Fatalf("wrong coverage: %+v", result)
		}
		payload, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), `"relatedIssues":null`) {
			t.Fatalf("null issue list: %s", payload)
		}
		if got := len(result["relatedIssues"].([]issues.Issue)); got != min(n, 3) {
			t.Fatalf("rows=%d want %d", got, min(n, 3))
		}
	}
}
