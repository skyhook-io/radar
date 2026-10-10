package k8score

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

type DeleteResourceResult struct {
	DeletionTimestamp *metav1.Time `json:"deletionTimestamp,omitempty"`
	PendingFinalizers []string     `json:"pendingFinalizers,omitempty"`
	ObservationError  string       `json:"observationError,omitempty"`
}

// ObserveResourceDeletion cannot turn an accepted DELETE into a failure. A GET
// may be denied independently, or may already see a replacement object.
func ObserveResourceDeletion(ctx context.Context, client dynamic.ResourceInterface, name string, uid types.UID) *DeleteResourceResult {
	result := &DeleteResourceResult{}
	obj, err := client.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return result
	}
	if err != nil {
		result.ObservationError = err.Error()
		return result
	}
	if uid != "" && obj.GetUID() != uid {
		return result
	}
	if obj.GetDeletionTimestamp() != nil {
		result.DeletionTimestamp = obj.GetDeletionTimestamp()
		result.PendingFinalizers = obj.GetFinalizers()
	}
	return result
}
