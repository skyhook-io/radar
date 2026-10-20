package cnpg

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestCNPGOperatorEventsBindCurrentObjectsAndRetainRolloutHistory(t *testing.T) {
	d := operatorDeployment(1, 1)
	d.UID = "deployment-current"
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "operator-pod", Namespace: d.Namespace, UID: "pod-current"}}
	var events []runtime.Object
	for i, subject := range []corev1.ObjectReference{
		{Kind: "Pod", Name: pod.Name, UID: pod.UID},
		{Kind: "Pod", Name: pod.Name, UID: "pod-old"},
		{Kind: "Deployment", Name: d.Name, UID: d.UID},
		{Kind: "Deployment", Name: d.Name, UID: "deployment-old"},
		{Kind: "ReplicaSet", Name: d.Name + "-old-revision", UID: "historical-revision"},
		{Kind: "Lease", Name: cnpgOperatorLeaseName, UID: "lease"},
	} {
		events = append(events, &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("event-%d", i), Namespace: d.Namespace}, InvolvedObject: subject, Reason: string(subject.UID)})
	}
	out := newTestReader(nil).operatorEvents(context.Background(), k8sfake.NewSimpleClientset(events...), d, []corev1.Pod{pod})
	if out.State != cnpgReadOK || len(out.Items) != 4 {
		t.Fatalf("events: %+v", out)
	}
	seen := map[string]bool{}
	for _, e := range out.Items {
		seen[e.Reason] = true
	}
	for _, uid := range []string{"pod-current", "deployment-current", "historical-revision", "lease"} {
		if !seen[uid] {
			t.Errorf("event missing: %s", uid)
		}
	}
}
