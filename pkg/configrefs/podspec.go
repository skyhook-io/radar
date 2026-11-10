package configrefs

import (
	"cmp"
	"slices"

	corev1 "k8s.io/api/core/v1"
)

// PodSpecReference describes a declared reference, without resolving admission
// defaults or asserting that its target exists.
type PodSpecReference struct {
	Ref
	Path     string
	Optional bool
}

// PodSpecReferences retains distinct optionality at a shared path because
// different containers can declare the same target with different requirements.
func PodSpecReferences(namespace string, spec corev1.PodSpec) []PodSpecReference {
	seen := map[PodSpecReference]bool{}
	VisitPodSpecReferences(namespace, spec, func(ref PodSpecReference) { seen[ref] = true })
	result := make([]PodSpecReference, 0, len(seen))
	for ref := range seen {
		result = append(result, ref)
	}
	slices.SortFunc(result, func(a, b PodSpecReference) int {
		if order := cmp.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Path, b.Path); order != 0 {
			return order
		}
		if a.Optional == b.Optional {
			return 0
		}
		if a.Optional {
			return 1
		}
		return -1
	})
	return result
}

// VisitPodSpecReferences visits non-empty declarations without collecting or
// sorting them. Repeated declarations are visited separately; callers that need
// unique references should use PodSpecReferences or deduplicate their target keys.
func VisitPodSpecReferences(namespace string, spec corev1.PodSpec, visit func(PodSpecReference)) {
	add := func(kind, name, path string, optional *bool) {
		if name != "" {
			visit(PodSpecReference{Ref: Ref{kind, namespace, name}, Path: path, Optional: optional != nil && *optional})
		}
	}
	collectEnv := func(paths [4]string, env []corev1.EnvVar, envFrom []corev1.EnvFromSource) {
		for _, source := range envFrom {
			if ref := source.ConfigMapRef; ref != nil {
				add("ConfigMap", ref.Name, paths[0], ref.Optional)
			}
			if ref := source.SecretRef; ref != nil {
				add("Secret", ref.Name, paths[1], ref.Optional)
			}
		}
		for _, variable := range env {
			if variable.ValueFrom == nil {
				continue
			}
			if ref := variable.ValueFrom.ConfigMapKeyRef; ref != nil {
				add("ConfigMap", ref.Name, paths[2], ref.Optional)
			}
			if ref := variable.ValueFrom.SecretKeyRef; ref != nil {
				add("Secret", ref.Name, paths[3], ref.Optional)
			}
		}
	}
	for _, container := range spec.Containers {
		collectEnv([4]string{"containers[].envFrom[].configMapRef.name", "containers[].envFrom[].secretRef.name", "containers[].env[].valueFrom.configMapKeyRef.name", "containers[].env[].valueFrom.secretKeyRef.name"}, container.Env, container.EnvFrom)
	}
	for _, container := range spec.InitContainers {
		collectEnv([4]string{"initContainers[].envFrom[].configMapRef.name", "initContainers[].envFrom[].secretRef.name", "initContainers[].env[].valueFrom.configMapKeyRef.name", "initContainers[].env[].valueFrom.secretKeyRef.name"}, container.Env, container.EnvFrom)
	}
	for _, container := range spec.EphemeralContainers {
		collectEnv([4]string{"ephemeralContainers[].envFrom[].configMapRef.name", "ephemeralContainers[].envFrom[].secretRef.name", "ephemeralContainers[].env[].valueFrom.configMapKeyRef.name", "ephemeralContainers[].env[].valueFrom.secretKeyRef.name"}, container.Env, container.EnvFrom)
	}
	for _, volume := range spec.Volumes {
		if ref := volume.ConfigMap; ref != nil {
			add("ConfigMap", ref.Name, "volumes[].configMap.name", ref.Optional)
		}
		if ref := volume.Secret; ref != nil {
			add("Secret", ref.SecretName, "volumes[].secret.secretName", ref.Optional)
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if ref := source.ConfigMap; ref != nil {
					add("ConfigMap", ref.Name, "volumes[].projected.sources[].configMap.name", ref.Optional)
				}
				if ref := source.Secret; ref != nil {
					add("Secret", ref.Name, "volumes[].projected.sources[].secret.name", ref.Optional)
				}
			}
		}
		if ref := volume.PersistentVolumeClaim; ref != nil {
			add("PersistentVolumeClaim", ref.ClaimName, "volumes[].persistentVolumeClaim.claimName", nil)
		}
		if volume.CSI != nil && volume.CSI.NodePublishSecretRef != nil {
			add("Secret", volume.CSI.NodePublishSecretRef.Name, "volumes[].csi.nodePublishSecretRef.name", nil)
		}
		if volume.FlexVolume != nil && volume.FlexVolume.SecretRef != nil {
			add("Secret", volume.FlexVolume.SecretRef.Name, "volumes[].flexVolume.secretRef.name", nil)
		}
		if volume.AzureFile != nil {
			add("Secret", volume.AzureFile.SecretName, "volumes[].azureFile.secretName", nil)
		}
		if volume.CephFS != nil && volume.CephFS.SecretRef != nil {
			add("Secret", volume.CephFS.SecretRef.Name, "volumes[].cephfs.secretRef.name", nil)
		}
		if volume.RBD != nil && volume.RBD.SecretRef != nil {
			add("Secret", volume.RBD.SecretRef.Name, "volumes[].rbd.secretRef.name", nil)
		}
		if volume.Cinder != nil && volume.Cinder.SecretRef != nil {
			add("Secret", volume.Cinder.SecretRef.Name, "volumes[].cinder.secretRef.name", nil)
		}
		if volume.ScaleIO != nil && volume.ScaleIO.SecretRef != nil {
			add("Secret", volume.ScaleIO.SecretRef.Name, "volumes[].scaleIO.secretRef.name", nil)
		}
		if volume.ISCSI != nil && volume.ISCSI.SecretRef != nil {
			add("Secret", volume.ISCSI.SecretRef.Name, "volumes[].iscsi.secretRef.name", nil)
		}
		if volume.StorageOS != nil && volume.StorageOS.SecretRef != nil {
			add("Secret", volume.StorageOS.SecretRef.Name, "volumes[].storageos.secretRef.name", nil)
		}
	}
	for _, ref := range spec.ImagePullSecrets {
		add("Secret", ref.Name, "imagePullSecrets[].name", nil)
	}
	add("ServiceAccount", spec.ServiceAccountName, "serviceAccountName", nil)
}
