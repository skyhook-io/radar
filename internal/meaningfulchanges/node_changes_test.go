package meaningfulchanges

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

func TestRecentNodeConfigurationRequiresKindFilter(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatal(err)
	}
	oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Annotations: map[string]string{"projectcalico.org/IPv4Address": "10.0.0.1/24"}},
		Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}}}
	for _, field := range []string{"label", "annotation", "taint value", "taint effect"} {
		newNode := oldNode.DeepCopy()
		switch field {
		case "label":
			newNode.Labels = map[string]string{"topology.kubernetes.io/zone": "east"}
		case "annotation":
			newNode.Annotations["projectcalico.org/IPv4Address"] = "10.0.0.2/24"
		case "taint value":
			newNode.Spec.Taints[0].Value = "batch"
		case "taint effect":
			newNode.Spec.Taints[0].Effect = corev1.TaintEffectNoExecute
		}
		diff := k8s.ComputeDiff("Node", oldNode, newNode)
		if diff == nil {
			t.Fatalf("missing %s diff", field)
		}
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: field, Timestamp: time.Now(), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Node", APIVersion: "v1", Name: "worker", EventType: timeline.EventTypeUpdate, Diff: diff,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Karpenter sets the Node's owner to its NodeClaim; an owned Node's changes
	// are still the Node's own.
	owned := oldNode.DeepCopy()
	owned.Labels = map[string]string{"team": "payments"}
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
		ID: "owned", Timestamp: time.Now(), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Node", APIVersion: "v1", Name: "karpenter-node", EventType: timeline.EventTypeUpdate,
		Owner: &timeline.OwnerInfo{Kind: "NodeClaim", Name: "pool-abc12"}, Diff: k8s.ComputeDiff("Node", oldNode, owned),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recent(context.Background(), Query{Kinds: []string{"Node"}})
	if err != nil || len(result.Changes) != 5 {
		t.Fatalf("Recent(Node) = %+v, err=%v", result, err)
	}
	for _, change := range result.Changes {
		if change.Kind != "Node" || change.ChangeCategory != issuesapi.ChangeCategorySpecConfig || len(change.Fields) != 1 {
			t.Fatalf("missing or misclassified Node field: %+v", change)
		}
	}
	result, err = Recent(context.Background(), Query{})
	if err != nil || len(result.Changes) != 0 {
		t.Fatalf("Node leaked into default feed: %+v, err=%v", result, err)
	}
	if TrackedKind("Node") {
		t.Fatal("Node must remain outside the per-issue correlation kinds")
	}
}

func TestClassifyNodeMetadataDoesNotChangeOtherKinds(t *testing.T) {
	for _, kind := range []string{"Node", "Pod", "Deployment"} {
		for _, path := range []string{"metadata.labels[topology.kubernetes.io/zone]", "metadata.annotations[projectcalico.org/IPv4Address]"} {
			event := timeline.TimelineEvent{Source: timeline.SourceInformer, Kind: kind, EventType: timeline.EventTypeUpdate,
				Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: path}}}}
			category, _ := classify(event)
			want := issuesapi.ChangeCategory("")
			if kind == "Node" {
				want = issuesapi.ChangeCategorySpecConfig
			}
			if category != want {
				t.Fatalf("classify(%s, %s) = %q, want %q", kind, path, category, want)
			}
		}
	}
}
