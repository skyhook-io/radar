package portforward

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func relayPod(name string, ready bool, deleting bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system", Labels: map[string]string{"k8s-app": "hubble-relay"}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
	if deleting {
		now := metav1.Now()
		pod.DeletionTimestamp = &now
		pod.Finalizers = []string{"test"}
	}
	return pod
}

func relayService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "hubble-relay", Namespace: "kube-system"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.1", Selector: map[string]string{"k8s-app": "hubble-relay"}},
	}
}

func TestFindPodForServicePrefersAPodThatCanServe(t *testing.T) {
	cases := []struct {
		name string
		pods []*corev1.Pod
		want string
	}{
		{
			// The replica stuck terminating on a node that went away still
			// reads Running, and lists first.
			name: "skips a pod being deleted",
			pods: []*corev1.Pod{relayPod("a-terminating", true, true), relayPod("b-live", true, false)},
			want: "b-live",
		},
		{
			name: "prefers a ready pod",
			pods: []*corev1.Pod{relayPod("a-not-ready", false, false), relayPod("b-ready", true, false)},
			want: "b-ready",
		},
		{
			name: "takes a running pod that is not ready when none is",
			pods: []*corev1.Pod{relayPod("a-terminating", true, true), relayPod("b-not-ready", false, false)},
			want: "b-not-ready",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(relayService())
			for _, p := range tc.pods {
				if _, err := client.CoreV1().Pods("kube-system").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := FindPodForService(context.Background(), client, "kube-system", "hubble-relay")
			if err != nil {
				t.Fatalf("FindPodForService: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("only pods being deleted", func(t *testing.T) {
		client := fake.NewSimpleClientset(relayService(), relayPod("a-terminating", true, true))
		if got, err := FindPodForService(context.Background(), client, "kube-system", "hubble-relay"); err == nil {
			t.Errorf("got %q, want an error: no pod can serve", got)
		}
	})
}
