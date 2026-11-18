package k8score

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/configrefs"
)

// ReflectsIndex files Secrets and ConfigMaps by the namespace/name of the
// source their Reflector annotation names, so a source's mirrors, which can
// be in any namespace, are found without reading every Secret. Only mirrors
// get a key, so it holds next to nothing: an index that files every object
// of a busy kind, such as Pods by Node, costs each relist more than reading
// the kind costs the rare question it answers (docs/loadtest.md).
const ReflectsIndex = "reflects"

// ReflectsKey is the ReflectsIndex key for a source in namespace.
func ReflectsKey(namespace, name string) string { return namespace + "/" + name }

var relationIndexers = map[string]cache.Indexers{
	Secrets:    {ReflectsIndex: reflectedSource("Secret")},
	ConfigMaps: {ReflectsIndex: reflectedSource("ConfigMap")},
}

func reflectedSource(kind string) cache.IndexFunc {
	return func(obj any) ([]string, error) {
		var ns, name string
		var annotations map[string]string
		switch o := obj.(type) {
		case *corev1.Secret:
			ns, name, annotations = o.Namespace, o.Name, o.Annotations
		case *corev1.ConfigMap:
			ns, name, annotations = o.Namespace, o.Name, o.Annotations
		default:
			return nil, nil
		}
		if source, ok := configrefs.ReflectedSource(kind, ns, name, annotations); ok {
			return []string{ReflectsKey(source.Namespace, source.Name)}, nil
		}
		return nil, nil
	}
}
