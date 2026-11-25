package workloadrestart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/workloadrestart"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// PatchResult reports the marker admission returned. An unconditional StatefulSet
// or DaemonSet restart only requires patch permission, so it has no prior marker
// to compare. In that case Changed reports whether the patch stored its marker.
func PatchResult(before, after *unstructured.Unstructured, requested string, path ...string) workloadrestart.Result {
	stored, _, _ := unstructured.NestedString(after.Object, path...)
	result := workloadrestart.Result{RestartedAt: stored, Changed: stored == requested}
	if before != nil {
		previous, _, _ := unstructured.NestedString(before.Object, path...)
		result.Changed = result.Changed && previous != stored
	}
	return result
}

// Apply checks the reviewed identity and the workload's update behavior on each
// attempt. On ErrNotStored the result carries the marker returned by admission.
func Apply(ctx context.Context, resource dynamic.ResourceInterface, name string, review workloadrestart.Preconditions, check func(*unstructured.Unstructured) error, at time.Time, layout string, path ...string) (workloadrestart.Result, error) {
	if review.UID == "" {
		return workloadrestart.Result{}, errors.New("a reviewed workload UID is required")
	}
	if check == nil {
		return workloadrestart.Result{}, errors.New("a workload applicability check is required")
	}
	if len(path) == 0 {
		return workloadrestart.Result{}, errors.New("a restart marker path is required")
	}
	marker := at.UTC().Format(layout)
	const maxAttempts = 4
	obj, err := resource.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return workloadrestart.Result{}, err
	}
	for attempt := 1; ; attempt++ {
		version := obj.GetResourceVersion()
		if obj.GetUID() != review.UID || (review.ResourceVersion != "" && version != review.ResourceVersion) {
			return workloadrestart.Result{}, workloadrestart.ErrChanged
		}
		if err := check(obj); err != nil {
			return workloadrestart.Result{}, err
		}
		previous, _, _ := unstructured.NestedString(obj.Object, path...)
		// A later Rollout cutoff also covers pods older than the requested marker.
		if t, err := time.Parse(layout, previous); err == nil && (t.UTC().Format(layout) == marker || (obj.GetKind() == "Rollout" && t.After(at))) {
			return workloadrestart.Result{RestartedAt: previous}, nil
		}
		patch := map[string]any{"metadata": map[string]any{"uid": string(review.UID), "resourceVersion": version}}
		if err := unstructured.SetNestedField(patch, marker, path...); err != nil {
			return workloadrestart.Result{}, err
		}
		body, err := json.Marshal(patch)
		if err != nil {
			return workloadrestart.Result{}, err
		}
		updated, err := resource.Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{})
		if apierrors.IsConflict(err) {
			// Admission can also return Conflict. A fresh read distinguishes a
			// refusal on the same version from a concurrent workload update.
			latest, readErr := resource.Get(ctx, name, metav1.GetOptions{})
			if readErr != nil {
				return workloadrestart.Result{}, readErr
			}
			if latest.GetUID() != review.UID {
				return workloadrestart.Result{}, workloadrestart.ErrChanged
			}
			if latest.GetResourceVersion() == version {
				return workloadrestart.Result{}, err
			}
			if review.ResourceVersion != "" || attempt == maxAttempts {
				return workloadrestart.Result{}, workloadrestart.ErrChanged
			}
			obj = latest
			continue
		}
		if err != nil {
			if isImmutableUID(err) {
				return workloadrestart.Result{}, workloadrestart.ErrChanged
			}
			return workloadrestart.Result{}, err
		}
		result := PatchResult(obj, updated, marker, path...)
		if result.RestartedAt != marker {
			return result, fmt.Errorf("%w (stored %q)", workloadrestart.ErrNotStored, result.RestartedAt)
		}
		return result, nil
	}
}

func isImmutableUID(err error) bool {
	var status apierrors.APIStatus
	if !apierrors.IsInvalid(err) || !errors.As(err, &status) || status.Status().Details == nil {
		return false
	}
	for _, cause := range status.Status().Details.Causes {
		if cause.Type == metav1.CauseTypeFieldValueInvalid && cause.Field == "metadata.uid" && strings.HasSuffix(cause.Message, "field is immutable") {
			return true
		}
	}
	return false
}
