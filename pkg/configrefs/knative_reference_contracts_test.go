package configrefs

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestKnativeReferenceAlternatives(t *testing.T) {
	for _, tc := range []struct {
		version, group, wantGroup string
		valid                     bool
	}{
		{"v1", "", "", true}, {"serving.knative.dev/v1", "", "serving.knative.dev", true},
		{"", "eventing.knative.dev", "eventing.knative.dev", true},
		{"eventing.knative.dev/v1", "eventing.knative.dev", "eventing.knative.dev", true},
		{"v1", "serving.knative.dev", "", false}, {"", "", "", false}, {"group/", "", "", false},
	} {
		ref, valid := KnativeReference(map[string]any{"apiVersion": tc.version, "group": tc.group, "kind": "Service", "name": "web"}, "app")
		if valid != tc.valid || valid && (ref.Group != tc.wantGroup || ref.Namespace != "app") {
			t.Fatalf("%s/%s: %+v valid=%v", tc.version, tc.group, ref, valid)
		}
	}
}

func TestTriggerBrokerLookupIgnoresKReferenceTypeFields(t *testing.T) {
	ref, valid := KnativeTriggerBrokerReference(map[string]any{"namespace": "events", "name": "broker", "kind": "Service", "apiVersion": "v1"})
	if !valid || ref.Group != "eventing.knative.dev" || ref.Kind != "Broker" {
		t.Fatalf("ref=%+v", ref)
	}
	if _, valid := KnativeTriggerBrokerReference(map[string]any{"name": "broker"}); valid {
		t.Fatal("reconciler does not default missing brokerRef namespace")
	}
}

func TestKnativeTriggerBrokerPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec map[string]any
		want resourceid.Ref
		path string
	}{
		{"absent", map[string]any{}, resourceid.NewRef(KnativeEventingGroup, "Broker", "app", "local"), "spec.broker"},
		{"valid", map[string]any{"brokerRef": map[string]any{"namespace": "events", "name": "remote"}}, resourceid.NewRef(KnativeEventingGroup, "Broker", "events", "remote"), "spec.brokerRef"},
		{"null", map[string]any{"brokerRef": nil}, resourceid.Ref{}, ""},
		{"scalar", map[string]any{"brokerRef": "invalid"}, resourceid.Ref{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec["broker"] = "local"
			trigger := &unstructured.Unstructured{Object: map[string]any{"spec": tc.spec}}
			trigger.SetNamespace("app")
			got, ok := KnativeTriggerBroker(trigger)
			if ok != (tc.path != "") || got.Ref != tc.want || got.Path != tc.path {
				t.Errorf("KnativeTriggerBroker = (%+v, %v), want %v via %q", got, ok, tc.want, tc.path)
			}
		})
	}
}
