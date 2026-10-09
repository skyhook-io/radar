package cnpg

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

const Group = "postgresql.cnpg.io"

func ControlledBy(refs []metav1.OwnerReference, group, kind, name string, uid types.UID) bool {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return ref.Kind == kind && ref.Name == name && ref.UID == uid && resourceid.GroupFromAPIVersion(ref.APIVersion) == group
		}
	}
	return false
}

func IsInstancePod(pod *corev1.Pod, namespace, cluster string, uid types.UID) bool {
	return uid != "" && pod.Namespace == namespace && pod.Labels["cnpg.io/cluster"] == cluster && ControlledBy(pod.OwnerReferences, Group, "Cluster", cluster, uid)
}

func InstanceRole(pod *corev1.Pod) string {
	if role := pod.Labels["cnpg.io/instanceRole"]; role != "" {
		return role
	}
	return pod.Labels["role"]
}
