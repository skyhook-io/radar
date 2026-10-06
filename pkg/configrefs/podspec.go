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

func PodSpecReferences(namespace string, spec corev1.PodSpec) []PodSpecReference {
	seen := map[PodSpecReference]bool{}
	add := func(kind, name, path string, optional *bool) {
		if name == "" {
			return
		}
		seen[PodSpecReference{Ref: Ref{kind, namespace, name}, Path: path, Optional: optional != nil && *optional}] = true
	}
	collectEnv := func(prefix string, env []corev1.EnvVar, envFrom []corev1.EnvFromSource) {
		for _, source := range envFrom {
			if ref := source.ConfigMapRef; ref != nil {
				add("ConfigMap", ref.Name, prefix+".envFrom[].configMapRef.name", ref.Optional)
			}
			if ref := source.SecretRef; ref != nil {
				add("Secret", ref.Name, prefix+".envFrom[].secretRef.name", ref.Optional)
			}
		}
		for _, variable := range env {
			if variable.ValueFrom == nil {
				continue
			}
			if ref := variable.ValueFrom.ConfigMapKeyRef; ref != nil {
				add("ConfigMap", ref.Name, prefix+".env[].valueFrom.configMapKeyRef.name", ref.Optional)
			}
			if ref := variable.ValueFrom.SecretKeyRef; ref != nil {
				add("Secret", ref.Name, prefix+".env[].valueFrom.secretKeyRef.name", ref.Optional)
			}
		}
	}
	for _, container := range spec.Containers {
		collectEnv("containers[]", container.Env, container.EnvFrom)
	}
	for _, container := range spec.InitContainers {
		collectEnv("initContainers[]", container.Env, container.EnvFrom)
	}
	for _, container := range spec.EphemeralContainers {
		collectEnv("ephemeralContainers[]", container.Env, container.EnvFrom)
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
	result := make([]PodSpecReference, 0, len(seen))
	for ref := range seen {
		result = append(result, ref)
	}
	slices.SortFunc(result, func(a, b PodSpecReference) int {
		if order := cmp.Compare(a.Key(), b.Key()); order != 0 {
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
