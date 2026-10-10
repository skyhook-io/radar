package server

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func backendPod(name string, ready, deleting bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Labels: map[string]string{"app": "cart"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "cart",
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 7070}},
		}}},
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

func TestFindPodForServiceSkipsAPodBeingDeleted(t *testing.T) {
	cases := []struct {
		name       string
		clusterIP  string
		targetPort intstr.IntOrString
		wantPort   int
	}{
		{name: "numeric target port", clusterIP: "10.0.0.1", targetPort: intstr.FromInt32(7070), wantPort: 7070},
		{name: "named target port", clusterIP: "10.0.0.1", targetPort: intstr.FromString("http"), wantPort: 7070},
		{name: "headless", clusterIP: corev1.ClusterIPNone, targetPort: intstr.FromInt32(7070), wantPort: 80},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop"},
				Spec: corev1.ServiceSpec{
					ClusterIP: tc.clusterIP,
					Selector:  map[string]string{"app": "cart"},
					Ports:     []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: tc.targetPort}},
				},
			}
			// The pod being deleted still reads Running and Ready, and lists first.
			client := fake.NewClientset(svc,
				backendPod("a-terminating", true, true),
				backendPod("b-not-ready", false, false),
				backendPod("c-ready", true, false))

			pod, port, _, err := findPodForService(context.Background(), client, "shop", "cart", 80)
			if err != nil {
				t.Fatal(err)
			}
			if pod != "c-ready" || port != tc.wantPort {
				t.Fatalf("got %s:%d, want c-ready:%d", pod, port, tc.wantPort)
			}
		})
	}
}
