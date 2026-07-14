package server

import (
	"reflect"
	"strings"

	"github.com/skyhook-io/radar/pkg/topology"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func filterManagedResources(result any, kind string, topo *topology.Topology) any {
	v := reflect.ValueOf(result)
	if !v.IsValid() || v.Kind() != reflect.Slice {
		return result
	}
	out := reflect.MakeSlice(v.Type(), 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		item := v.Index(i)
		var obj metav1.Object
		if item.Kind() == reflect.Interface && !item.IsNil() {
			item = item.Elem()
		}
		if item.CanInterface() {
			obj, _ = item.Interface().(metav1.Object)
		}
		if obj == nil || topology.IsManagedResource(topo, strings.TrimSuffix(kind, "s"), obj.GetNamespace(), obj.GetName(), obj.GetLabels(), obj.GetAnnotations()) {
			out = reflect.Append(out, v.Index(i))
		}
	}
	return out.Interface()
}
