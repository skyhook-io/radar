package cnpg

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInstanceIdentity(t *testing.T) {
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Labels: map[string]string{"cnpg.io/cluster": "pg"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: Group + "/v1", Kind: "Cluster", Name: "pg", UID: "current", Controller: &controller}}}}
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.Pod)
		want   bool
	}{
		{"current", func(*corev1.Pod) {}, true},
		{"old cluster UID", func(p *corev1.Pod) { p.OwnerReferences[0].UID = "old" }, false},
		{"other group", func(p *corev1.Pod) { p.OwnerReferences[0].APIVersion = "cluster.x-k8s.io/v1beta1" }, false},
		{"other namespace", func(p *corev1.Pod) { p.Namespace = "other" }, false},
		{"label only", func(p *corev1.Pod) { p.OwnerReferences = nil }, false},
		{"backup Job", func(p *corev1.Pod) { p.OwnerReferences[0].Kind = "Job" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := pod.DeepCopy()
			tc.mutate(p)
			if got := IsInstancePod(p, "db", "pg", "current"); got != tc.want {
				t.Fatalf("instance = %v", got)
			}
		})
	}
}
