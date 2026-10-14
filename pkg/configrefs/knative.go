package configrefs

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

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
