package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetChangesNodeConfiguration(t *testing.T) {
	store := initCorrelationStore(t)
	oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Annotations: map[string]string{"projectcalico.org/IPv4Address": "10.0.0.1/24"}},
		Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}}}
	for _, field := range []string{"label", "annotation", "taint"} {
		newNode := oldNode.DeepCopy()
		path := ""
		switch field {
		case "label":
			newNode.Labels = map[string]string{"topology.kubernetes.io/zone": "east"}
			path = "metadata.labels[topology.kubernetes.io/zone]"
		case "annotation":
			newNode.Annotations["projectcalico.org/IPv4Address"] = "10.0.0.2/24"
			path = "metadata.annotations[projectcalico.org/IPv4Address]"
		case "taint":
			newNode.Spec.Taints[0].Value = "batch"
			path = "spec.taints[dedicated]"
		}
		diff := k8s.ComputeDiff("Node", oldNode, newNode)
		if diff == nil {
			t.Fatalf("missing %s diff", field)
		}
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: field, Timestamp: time.Now(), Kind: "Node", APIVersion: "v1", Name: field,
			EventType: timeline.EventTypeUpdate, Diff: diff,
		})
		response, _ := getChangesForTest(t, context.Background(), getChangesInput{Kind: "Node", Name: field})
		if len(response.Changes) != 1 {
			t.Fatalf("get_changes kind=Node name=%s: %+v", field, response)
		}
		change := response.Changes[0]
		if change.ChangeCategory != issuesapi.ChangeCategorySpecConfig || len(change.Fields) != 1 || change.Fields[0].Path != path {
			t.Fatalf("missing or misclassified %s row: %+v", field, change)
		}
	}
	response, _ := getChangesForTest(t, context.Background(), getChangesInput{Kind: "Node"})
	if len(response.Changes) != 3 {
		t.Fatalf("get_changes kind=Node lost rows: %+v", response)
	}
	response, _ = getChangesForTest(t, context.Background(), getChangesInput{})
	if len(response.Changes) != 0 || len(response.Created) != 0 {
		t.Fatalf("Node leaked into default get_changes feed: %+v", response)
	}
}
