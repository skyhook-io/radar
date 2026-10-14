package configrefs

import "testing"

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
