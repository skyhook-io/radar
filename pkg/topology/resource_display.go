package topology

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func persistentVolumeClaimNode(pvc *corev1.PersistentVolumeClaim) Node {
	pvcID := fmt.Sprintf("persistentvolumeclaim/%s/%s", pvc.Namespace, pvc.Name)
	var storageSize string
	if pvc.Spec.Resources.Requests != nil {
		if storage, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			storageSize = storage.String()
		}
	}

	var storageClass string
	if pvc.Spec.StorageClassName != nil {
		storageClass = *pvc.Spec.StorageClassName
	}

	return Node{
		ID:     pvcID,
		Kind:   KindPVC,
		Name:   pvc.Name,
		Status: getPVCStatus(pvc),
		Data: map[string]any{
			"namespace":    pvc.Namespace,
			"storageClass": storageClass,
			"accessModes":  pvc.Spec.AccessModes,
			"storage":      storageSize,
			"phase":        string(pvc.Status.Phase),
			"labels":       pvc.Labels,
		},
	}
}

func serviceAccountNode(serviceAccount *corev1.ServiceAccount) Node {
	return Node{ID: fmt.Sprintf("serviceaccount/%s/%s", serviceAccount.Namespace, serviceAccount.Name), Kind: KindServiceAccount, Name: serviceAccount.Name, Status: StatusHealthy, Data: map[string]any{"namespace": serviceAccount.Namespace, "labels": serviceAccount.Labels}}
}
func sealedSecretNode(secret *unstructured.Unstructured) Node {
	return Node{ID: fmt.Sprintf("sealedsecret/%s/%s", secret.GetNamespace(), secret.GetName()), Kind: KindSealedSecret, Name: secret.GetName(), Status: sealedSecretHealth(secret), Data: map[string]any{"namespace": secret.GetNamespace(), "targetSecret": secret.GetName(), "labels": secret.GetLabels(), "apiVersion": secret.GetAPIVersion()}}
}
