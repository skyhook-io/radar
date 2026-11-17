package configrefs

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

const (
	KnativeServingGroup   = "serving.knative.dev"
	KnativeEventingGroup  = "eventing.knative.dev"
	KnativeMessagingGroup = "messaging.knative.dev"
	KnativeSourcesGroup   = "sources.knative.dev"
)

// KnativeSourceKinds are the event sources Knative Eventing ships.
var KnativeSourceKinds = []string{"PingSource", "ApiServerSource", "ContainerSource", "SinkBinding"}

// KnativeReference resolves KReference's apiVersion/group alternatives. The
// alpha group field is explicit evidence, not a kind-based default. If both
// are recorded they must agree. Empty group alone does not establish core.
func KnativeReference(raw map[string]any, defaultNamespace string) (resourceid.Ref, bool) {
	version, _ := raw["apiVersion"].(string)
	group, _ := raw["group"].(string)
	if version != "" {
		gv, err := schema.ParseGroupVersion(version)
		if err != nil || gv.Version == "" || group != "" && group != gv.Group {
			return resourceid.Ref{}, false
		}
		group = gv.Group
	} else if group == "" {
		return resourceid.Ref{}, false
	}
	kind, _ := raw["kind"].(string)
	name, _ := raw["name"].(string)
	namespace, _ := raw["namespace"].(string)
	if namespace == "" {
		namespace = defaultNamespace
	}
	if kind == "" || name == "" {
		return resourceid.Ref{}, false
	}
	return resourceid.NewRef(group, kind, namespace, name), true
}

// KnativeTriggerBrokerReference follows the broker reconciler's actual lookup:
// brokerRef's namespace/name identify an eventing Broker; kind/version are ignored.
// Unlike subscriber KReferences, the reconciler does not default its namespace.
func KnativeTriggerBrokerReference(raw map[string]any) (resourceid.Ref, bool) {
	name, _ := raw["name"].(string)
	namespace, _ := raw["namespace"].(string)
	if name == "" || namespace == "" {
		return resourceid.Ref{}, false
	}
	return resourceid.NewRef("eventing.knative.dev", "Broker", namespace, name), true
}

// KnativeEventDestinations returns where a Knative object sends events: a
// Trigger's subscriber, an event source's sink, or the backing channel a
// Channel's status records. A destination may name another namespace.
func KnativeEventDestinations(obj *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	VisitKnativeEventDestinations(obj, func(ref PathRef) { refs = append(refs, ref) })
	return refs
}

// VisitKnativeEventDestinations visits references without retaining a slice.
func VisitKnativeEventDestinations(obj *unstructured.Unstructured, visit func(PathRef)) {
	group := resourceid.GroupFromAPIVersion(obj.GetAPIVersion())
	var fields []string
	switch {
	case group == KnativeEventingGroup && obj.GetKind() == "Trigger":
		fields = []string{"spec", "subscriber", "ref"}
	case group == KnativeSourcesGroup:
		fields = []string{"spec", "sink", "ref"}
	case group == KnativeMessagingGroup && (obj.GetKind() == "Channel" || obj.GetKind() == "InMemoryChannel"):
		fields = []string{"status", "channel"}
	default:
		return
	}
	raw, present, err := borrowedMap(obj.Object, fields...)
	if err != nil || !present {
		return
	}
	ref, ok := KnativeReference(raw, obj.GetNamespace())
	if !ok {
		return
	}
	visit(PathRef{Ref: ref, Path: strings.Join(fields, ".")})
}

// KnativeTriggerBroker returns the Broker a Trigger subscribes to: its
// brokerRef, which names the Broker's namespace, or else the older
// spec.broker in the Trigger's namespace.
func KnativeTriggerBroker(trigger *unstructured.Unstructured) (PathRef, bool) {
	if raw, present, _ := borrowedMap(trigger.Object, "spec", "brokerRef"); present {
		ref, ok := KnativeTriggerBrokerReference(raw)
		return PathRef{Ref: ref, Path: "spec.brokerRef"}, ok
	}
	name, _, _ := unstructured.NestedString(trigger.Object, "spec", "broker")
	if name == "" {
		return PathRef{}, false
	}
	return PathRef{Ref: resourceid.NewRef(KnativeEventingGroup, "Broker", trigger.GetNamespace(), name), Path: "spec.broker"}, true
}

// KnativeRouteRevisions returns the Revisions a Knative Route sends traffic
// to, as its status resolves them: spec.traffic may name a Configuration.
func KnativeRouteRevisions(route *unstructured.Unstructured) []PathRef {
	targets, _, _ := borrowedSlice(route.Object, "status", "traffic")
	var refs []PathRef
	for _, t := range targets {
		m, _ := t.(map[string]any)
		if name, _ := m["revisionName"].(string); name != "" {
			refs = append(refs, PathRef{Ref: resourceid.NewRef(KnativeServingGroup, "Revision", route.GetNamespace(), name), Path: "status.traffic[].revisionName"})
		}
	}
	return refs
}
