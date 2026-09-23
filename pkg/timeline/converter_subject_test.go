package timeline

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// kubelet records Node events in "default" with the node's name as its UID;
// the row must describe the cluster-scoped Node and claim no incarnation, so it
// joins the Node's own rows instead of a phantom "default" Node.
func TestK8sEventRowDescribesItsSubject(t *testing.T) {
	nodeEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "node-a.1", UID: "evt-1"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Node", APIVersion: "v1", Name: "node-a", UID: types.UID("node-a"),
		},
		Reason: "NodeNotReady",
	}
	row := NewK8sEventTimelineEvent(nodeEvent, nil)
	if row.ID != "evt-1" || row.Kind != "Node" || row.Name != "node-a" {
		t.Fatalf("row identity = %+v", row)
	}
	if row.Namespace != "" || row.UID != "" {
		t.Fatalf("Node event row = namespace %q uid %q, want cluster-scoped with no incarnation", row.Namespace, row.UID)
	}

	podEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web.1", UID: "evt-2"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", APIVersion: "v1", Namespace: "shop", Name: "web", UID: types.UID("pod-uid"),
		},
	}
	if row := NewK8sEventTimelineEvent(podEvent, nil); row.Namespace != "shop" || row.UID != "pod-uid" {
		t.Fatalf("Pod event row = namespace %q uid %q", row.Namespace, row.UID)
	}
}
