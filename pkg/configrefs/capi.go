package configrefs

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// CAPIGroup is Cluster API's group.
const CAPIGroup = "cluster.x-k8s.io"

// CAPIClusterClass returns the ClusterClass a Cluster API Cluster's topology
// names, in the namespace it names or the Cluster's own. v1beta2 names it in
// topology.classRef; earlier versions in topology.class and
// topology.classNamespace.
func CAPIClusterClass(cluster *unstructured.Unstructured) (PathRef, bool) {
	path := "spec.topology.classRef"
	name, _, _ := unstructured.NestedString(cluster.Object, "spec", "topology", "classRef", "name")
	namespace, _, _ := unstructured.NestedString(cluster.Object, "spec", "topology", "classRef", "namespace")
	if name == "" {
		path = "spec.topology.class"
		name, _, _ = unstructured.NestedString(cluster.Object, "spec", "topology", "class")
		namespace, _, _ = unstructured.NestedString(cluster.Object, "spec", "topology", "classNamespace")
	}
	if name == "" {
		return PathRef{}, false
	}
	if namespace == "" {
		namespace = cluster.GetNamespace()
	}
	return PathRef{Ref: resourceid.NewRef(CAPIGroup, "ClusterClass", namespace, name), Path: path}, true
}
