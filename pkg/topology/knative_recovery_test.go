package topology

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestKnativeObservedBackingChannelAndGroupReference(t *testing.T) {
	sourceGVR := schema.GroupVersionResource{Group: "sources.knative.dev", Version: "v1", Resource: "pingsources"}
	channelGVR := schema.GroupVersionResource{Group: "messaging.knative.dev", Version: "v1", Resource: "channels"}
	memoryGVR := schema.GroupVersionResource{Group: "messaging.knative.dev", Version: "v1", Resource: "inmemorychannels"}
	for _, tc := range []struct {
		name string
		ref  map[string]any
		want bool
	}{
		{"group only", map[string]any{"group": "messaging.knative.dev", "kind": "InMemoryChannel", "name": "events"}, true},
		{"both agree", map[string]any{"apiVersion": "messaging.knative.dev/v1", "group": "messaging.knative.dev", "kind": "InMemoryChannel", "name": "events"}, true},
		{"both disagree", map[string]any{"apiVersion": "messaging.knative.dev/v1", "group": "other.example", "kind": "InMemoryChannel", "name": "events"}, false},
		{"missing identity", map[string]any{"kind": "InMemoryChannel", "name": "events"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := genericIdentityObject(sourceGVR, "PingSource", "app", "timer")
			source.Object["spec"] = map[string]any{"sink": map[string]any{"ref": tc.ref}}
			channel := genericIdentityObject(channelGVR, "Channel", "app", "events")
			channel.Object["status"] = map[string]any{"channel": map[string]any{"apiVersion": "messaging.knative.dev/v1", "kind": "InMemoryChannel", "name": "events"}}
			memory := genericIdentityObject(memoryGVR, "InMemoryChannel", "app", "events")
			d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{sourceGVR, channelGVR, memoryGVR}, kinds: map[schema.GroupVersionResource]string{sourceGVR: "PingSource", channelGVR: "Channel", memoryGVR: "InMemoryChannel"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{sourceGVR: {source}, channelGVR: {channel}, memoryGVR: {memory}}, listCalls: map[schema.GroupVersionResource]int{}}
			topo, err := NewBuilder(&mockProvider{}).WithDynamic(d).Build(DefaultBuildOptions())
			if err != nil {
				t.Fatal(err)
			}
			foundSource, foundBacking := false, false
			for _, edge := range topo.Edges {
				if edge.Source == "pingsource/app/timer" {
					if edge.Target != "inmemorychannel/app/events" {
						t.Fatalf("wrong destination: %+v", edge)
					}
					foundSource = true
				}
				if edge.Source == "channel/app/events" && edge.Target == "inmemorychannel/app/events" {
					if edge.Type != EdgeExposes {
						t.Fatalf("status-only reference must not assert ownership: %+v", edge)
					}
					foundBacking = true
				}
			}
			if foundSource != tc.want || !foundBacking {
				t.Fatalf("source=%v backing=%v", foundSource, foundBacking)
			}
		})
	}
}

func TestKnativeReferencePostpassUsesExactGenericNode(t *testing.T) {
	nodes := []Node{
		{ID: "pingsource/app/timer", Kind: KindPingSource, Name: "timer", Data: map[string]any{"namespace": "app", "apiVersion": "sources.knative.dev/v1"}},
		{ID: "jobsink/app/sink", Kind: NodeKind("JobSink"), Name: "sink", Data: map[string]any{"namespace": "app", "apiVersion": "sinks.knative.dev/v1alpha1"}},
		{ID: "jobsink/app/sink/other", Kind: NodeKind("JobSink"), Name: "sink", Data: map[string]any{"namespace": "app", "apiVersion": "other.example/v1"}},
	}
	source := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "sources.knative.dev/v1", "kind": "PingSource", "metadata": map[string]any{"namespace": "app", "name": "timer"}, "spec": map[string]any{"sink": map[string]any{"ref": map[string]any{"apiVersion": "sinks.knative.dev/v1alpha1", "kind": "JobSink", "name": "sink"}}}}}
	edges := addKnativeReferenceEdges(nodes, nil, nil, []*unstructured.Unstructured{source}, nil)
	if len(edges) != 1 || edges[0].Target != "jobsink/app/sink" {
		t.Fatalf("edges=%+v", edges)
	}
}

func TestTriggerBrokerAndObservedChannelOwnership(t *testing.T) {
	nodes := []Node{
		{ID: "trigger/app/t", Kind: KindTrigger, Name: "t", Data: map[string]any{"namespace": "app", "apiVersion": "eventing.knative.dev/v1"}},
		{ID: "broker/events/b", Kind: KindBroker, Name: "b", Data: map[string]any{"namespace": "events", "apiVersion": "eventing.knative.dev/v1"}},
		{ID: "service/events/b", Kind: KindService, Name: "b", Data: map[string]any{"namespace": "events"}},
		{ID: "channel/app/c", Kind: KindChannel, Name: "c", Data: map[string]any{"namespace": "app", "apiVersion": "messaging.knative.dev/v1"}},
		{ID: "inmemorychannel/app/c", Kind: NodeKind("InMemoryChannel"), Name: "c", Data: map[string]any{"namespace": "app", "apiVersion": "messaging.knative.dev/v1"}},
	}
	trigger := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"namespace": "app", "name": "t"}, "spec": map[string]any{"brokerRef": map[string]any{"namespace": "events", "name": "b", "kind": "Service", "apiVersion": "v1"}}}}
	channel := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "messaging.knative.dev/v1", "kind": "InMemoryChannel", "metadata": map[string]any{"namespace": "app", "name": "c", "ownerReferences": []any{map[string]any{"apiVersion": "messaging.knative.dev/v1", "kind": "Channel", "name": "c"}}}}}
	edges := addKnativeReferenceEdges(nodes, nil, []*unstructured.Unstructured{trigger}, nil, []*unstructured.Unstructured{channel})
	if len(edges) != 2 || edges[0].Source != "broker/events/b" || edges[1].Type != EdgeManages {
		t.Fatalf("edges=%+v", edges)
	}
}

func TestBuilderReadsBackingChannelOwnerBeforeStatusReference(t *testing.T) {
	channelGVR := schema.GroupVersionResource{Group: "messaging.knative.dev", Version: "v1", Resource: "channels"}
	memoryGVR := schema.GroupVersionResource{Group: "messaging.knative.dev", Version: "v1", Resource: "inmemorychannels"}
	parent := genericIdentityObject(channelGVR, "Channel", "app", "events")
	parent.Object["status"] = map[string]any{"channel": map[string]any{"apiVersion": "messaging.knative.dev/v1", "kind": "InMemoryChannel", "name": "events"}}
	backing := genericIdentityObject(memoryGVR, "InMemoryChannel", "app", "events")
	ctrl := true
	backing.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "messaging.knative.dev/v1", Kind: "Channel", Name: "events", Controller: &ctrl}})
	dp := &genericIdentityDynamic{watched: []schema.GroupVersionResource{channelGVR, memoryGVR}, kinds: map[schema.GroupVersionResource]string{channelGVR: "Channel", memoryGVR: "InMemoryChannel"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{channelGVR: {parent}, memoryGVR: {backing}}, listCalls: map[schema.GroupVersionResource]int{}}
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dp).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, edge := range topo.Edges {
		if edge.Source == "channel/app/events" && edge.Target == "inmemorychannel/app/events" {
			if edge.Type != EdgeManages {
				t.Fatalf("actual owner reduced to exposure: %+v", edge)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing actual backing-channel ownership")
	}
	rel := GetRelationshipsWithObject("InMemoryChannel", "app", "events", backing, topo, nil, dp, IndexByResource(topo))
	if rel == nil || rel.Owner == nil || rel.Owner.Kind != "Channel" {
		t.Fatalf("owner=%+v", rel)
	}
}
