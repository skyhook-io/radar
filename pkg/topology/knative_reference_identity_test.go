package topology

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestKnativeReferencesUseExactIdentity(t *testing.T) {
	serving := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	broker := schema.GroupVersionResource{Group: "eventing.knative.dev", Version: "v1", Resource: "brokers"}
	channel := schema.GroupVersionResource{Group: "messaging.knative.dev", Version: "v1", Resource: "channels"}
	trigger := schema.GroupVersionResource{Group: "eventing.knative.dev", Version: "v1", Resource: "triggers"}
	source := schema.GroupVersionResource{Group: "sources.knative.dev", Version: "v1", Resource: "pingsources"}
	for _, tc := range []struct{ apiVersion, kind, target string }{
		{"v1", "Service", "service/target/shared"},
		{"serving.knative.dev/v1", "Service", "knativeservice/target/shared"},
		{"eventing.knative.dev/v1", "Broker", "broker/target/shared"},
		{"messaging.knative.dev/v1", "Channel", "channel/target/shared"},
		{"other.example/v1", "Service", ""},
		{"messaging.knative.dev/v1", "InMemoryChannel", ""},
		{"v1", "Unknown", ""}, {"", "Service", ""},
	} {
		t.Run(tc.apiVersion+"/"+tc.kind, func(t *testing.T) {
			d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{serving, broker, channel, trigger, source}, kinds: map[schema.GroupVersionResource]string{serving: "Service", broker: "Broker", channel: "Channel", trigger: "Trigger", source: "PingSource"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{}, listCalls: map[schema.GroupVersionResource]int{}}
			for gvr, kind := range d.kinds {
				d.resources[gvr] = []*unstructured.Unstructured{genericIdentityObject(gvr, kind, "target", "shared")}
			}
			ref := map[string]any{"apiVersion": tc.apiVersion, "kind": tc.kind, "name": "shared", "namespace": "target"}
			tr := genericIdentityObject(trigger, "Trigger", "app", "trigger")
			tr.Object["spec"] = map[string]any{"subscriber": map[string]any{"ref": ref}}
			src := genericIdentityObject(source, "PingSource", "app", "source")
			src.Object["spec"] = map[string]any{"sink": map[string]any{"ref": ref}}
			d.resources[trigger] = []*unstructured.Unstructured{tr}
			d.resources[source] = []*unstructured.Unstructured{src}
			topo, err := NewBuilder(&mockProvider{services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Namespace: "target", Name: "shared"}}}}).WithDynamic(d).Build(DefaultBuildOptions())
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"trigger/app/trigger", "pingsource/app/source"} {
				var targets []string
				for _, e := range topo.Edges {
					if e.Source == id {
						targets = append(targets, e.Target)
					}
				}
				if tc.target == "" {
					if len(targets) != 0 {
						t.Errorf("%s joined %v", id, targets)
					}
				} else if len(targets) != 1 || targets[0] != tc.target {
					t.Errorf("%s targets=%v want %s", id, targets, tc.target)
				}
			}
		})
	}
}
