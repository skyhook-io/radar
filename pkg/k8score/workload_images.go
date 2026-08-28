package k8score

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/rollouts"
)

var (
	ErrUnsupportedImageWorkload = errors.New("image updates are not supported for this workload kind")
	ErrInvalidImageUpdate       = errors.New("invalid image update")
	ErrImageWorkloadTerminating = errors.New("workload is terminating")
)

const (
	containerTypeRegular = "container"
	containerTypeInit    = "initContainer"
)

type WorkloadContainerImage struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Image string `json:"image"`
}

type WorkloadImageTarget struct {
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type WorkloadUpdateBehavior struct {
	Type        string `json:"type"`
	Partition   *int64 `json:"partition,omitempty"`
	AutoPromote *bool  `json:"autoPromote,omitempty"`
	Gated       *bool  `json:"gated,omitempty"`
}

type WorkloadImageInventory struct {
	Target     WorkloadImageTarget      `json:"target"`
	Containers []WorkloadContainerImage `json:"containers"`
	Behavior   WorkloadUpdateBehavior   `json:"behavior"`
}

type WorkloadImageUpdate struct {
	Type          string `json:"type"`
	Name          string `json:"name"`
	PreviousImage string `json:"previousImage"`
	Image         string `json:"image"`
}

type SetWorkloadImagesResult struct {
	WorkloadImageInventory
	Object *unstructured.Unstructured `json:"object"`
}

type workloadImageRoot struct {
	gvr          schema.GroupVersionResource
	templatePath []string
}

var workloadImageRoots = map[string]workloadImageRoot{
	"deployments": {
		gvr:          schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		templatePath: []string{"spec", "template"},
	},
	"statefulsets": {
		gvr:          schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"},
		templatePath: []string{"spec", "template"},
	},
	"daemonsets": {
		gvr:          schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"},
		templatePath: []string{"spec", "template"},
	},
	"rollouts": {
		gvr:          rollouts.GVR,
		templatePath: []string{"spec", "template"},
	},
}

type resolvedWorkloadImageTarget struct {
	normalizedKind string
	root           *unstructured.Unstructured
	target         *unstructured.Unstructured
	targetGVR      schema.GroupVersionResource
	targetName     string
	templatePath   []string
}

type imageLocation struct {
	typeName string
	name     string
	index    int
}

func (m *WorkloadManager) GetWorkloadImages(ctx context.Context, kind, namespace, name string) (*WorkloadImageInventory, error) {
	resolved, err := m.resolveWorkloadImageTarget(ctx, kind, namespace, name)
	if err != nil {
		return nil, err
	}
	return resolved.inventory()
}

func (m *WorkloadManager) SetWorkloadImages(ctx context.Context, kind, namespace, name string, updates []WorkloadImageUpdate) (*SetWorkloadImagesResult, error) {
	resolved, err := m.resolveWorkloadImageTarget(ctx, kind, namespace, name)
	if err != nil {
		return nil, err
	}
	if err := validateImageUpdates(updates); err != nil {
		return nil, err
	}

	patch, locations, err := buildImagePatch(resolved.target, resolved.targetGVR.GroupResource(), resolved.templatePath, updates)
	if err != nil {
		return nil, err
	}
	updated, err := m.patchWorkloadImages(ctx, resolved, patch, locations, updates)
	if err != nil {
		return nil, err
	}

	resolved.target = StripUnstructuredFields(updated)
	inventory, err := resolved.inventory()
	if err != nil {
		return nil, err
	}
	return &SetWorkloadImagesResult{WorkloadImageInventory: *inventory, Object: resolved.target}, nil
}

func (m *WorkloadManager) resolveWorkloadImageTarget(ctx context.Context, kind, namespace, name string) (*resolvedWorkloadImageTarget, error) {
	if m.dynClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}
	normalizedKind := NormalizeWorkloadKind(strings.ToLower(kind))
	rootInfo, ok := workloadImageRoots[normalizedKind]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedImageWorkload, kind)
	}

	root, err := m.dynClient.Resource(rootInfo.gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get workload: %w", err)
	}
	if !root.GetDeletionTimestamp().IsZero() {
		return nil, fmt.Errorf("%s %s/%s: %w", root.GetKind(), namespace, name, ErrImageWorkloadTerminating)
	}

	resolved := &resolvedWorkloadImageTarget{
		normalizedKind: normalizedKind,
		root:           root,
		target:         root,
		targetGVR:      rootInfo.gvr,
		targetName:     name,
		templatePath:   rootInfo.templatePath,
	}
	if normalizedKind != "rollouts" {
		return resolved, nil
	}

	target, err := rollouts.ResolveTemplateTarget(root)
	if err != nil {
		return nil, err
	}
	resolved.targetGVR = target.GVR
	resolved.targetName = target.Name
	resolved.templatePath = target.TemplatePath
	if target.GVR == rollouts.GVR && target.Name == name {
		return resolved, nil
	}

	resolved.target, err = m.dynClient.Resource(target.GVR).Namespace(namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get workload %s/%s referenced by Rollout %s: %w", namespace, target.Name, name, err)
	}
	if !resolved.target.GetDeletionTimestamp().IsZero() {
		return nil, fmt.Errorf("%s %s/%s referenced by Rollout %s: %w", resolved.target.GetKind(), namespace, target.Name, name, ErrImageWorkloadTerminating)
	}
	return resolved, nil
}

func (r *resolvedWorkloadImageTarget) inventory() (*WorkloadImageInventory, error) {
	containers, err := inventoryContainerImages(r.target, r.templatePath)
	if err != nil {
		return nil, err
	}
	return &WorkloadImageInventory{
		Target: WorkloadImageTarget{
			Group:     r.targetGVR.Group,
			Resource:  r.targetGVR.Resource,
			Kind:      r.target.GetKind(),
			Namespace: r.target.GetNamespace(),
			Name:      r.targetName,
		},
		Containers: containers,
		Behavior:   classifyWorkloadUpdateBehavior(r.normalizedKind, r.root),
	}, nil
}

func inventoryContainerImages(obj *unstructured.Unstructured, templatePath []string) ([]WorkloadContainerImage, error) {
	var images []WorkloadContainerImage
	for _, set := range []struct {
		typeName string
		field    string
	}{
		{typeName: containerTypeRegular, field: "containers"},
		{typeName: containerTypeInit, field: "initContainers"},
	} {
		path := append(append([]string{}, templatePath...), "spec", set.field)
		containers, found, err := unstructured.NestedSlice(obj.Object, path...)
		if err != nil {
			return nil, fmt.Errorf("%w: %s is malformed", ErrInvalidImageUpdate, strings.Join(path, "."))
		}
		if !found {
			if set.typeName == containerTypeRegular {
				return nil, fmt.Errorf("%w: workload has no containers", ErrInvalidImageUpdate)
			}
			continue
		}
		for _, raw := range containers {
			container, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: %s contains a malformed entry", ErrInvalidImageUpdate, strings.Join(path, "."))
			}
			name, nameOK := container["name"].(string)
			image, imageOK := container["image"].(string)
			if !nameOK || name == "" || !imageOK || image == "" {
				return nil, fmt.Errorf("%w: every container must have a name and image", ErrInvalidImageUpdate)
			}
			images = append(images, WorkloadContainerImage{Type: set.typeName, Name: name, Image: image})
		}
	}
	return images, nil
}

func validateImageUpdates(updates []WorkloadImageUpdate) error {
	if len(updates) == 0 {
		return fmt.Errorf("%w: at least one changed image is required", ErrInvalidImageUpdate)
	}
	if len(updates) > 64 {
		return fmt.Errorf("%w: at most 64 images can be changed at once", ErrInvalidImageUpdate)
	}
	seen := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		if update.Type != containerTypeRegular && update.Type != containerTypeInit {
			return fmt.Errorf("%w: unknown container type %q", ErrInvalidImageUpdate, update.Type)
		}
		if update.Name == "" || update.PreviousImage == "" || update.Image == "" {
			return fmt.Errorf("%w: container name, previous image, and new image are required", ErrInvalidImageUpdate)
		}
		if update.Image == update.PreviousImage {
			return fmt.Errorf("%w: image for %s is unchanged", ErrInvalidImageUpdate, update.Name)
		}
		key := update.Type + "\x00" + update.Name
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate update for %s", ErrInvalidImageUpdate, update.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func buildImagePatch(obj *unstructured.Unstructured, resource schema.GroupResource, templatePath []string, updates []WorkloadImageUpdate) ([]byte, []imageLocation, error) {
	operations := make([]map[string]any, 0, len(updates)*3)
	locations := make([]imageLocation, 0, len(updates))
	for _, update := range updates {
		field := "containers"
		if update.Type == containerTypeInit {
			field = "initContainers"
		}
		path := append(append([]string{}, templatePath...), "spec", field)
		containers, found, err := unstructured.NestedSlice(obj.Object, path...)
		if err != nil || !found {
			return nil, nil, missingImageConflict(resource, obj, update)
		}

		index := -1
		currentImage := ""
		for i, raw := range containers {
			container, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if container["name"] == update.Name {
				index = i
				currentImage, _ = container["image"].(string)
				break
			}
		}
		if index < 0 {
			return nil, nil, missingImageConflict(resource, obj, update)
		}
		if currentImage != update.PreviousImage {
			return nil, nil, imageConflict(resource, obj, update, currentImage)
		}

		basePath := "/" + strings.Join(path, "/") + fmt.Sprintf("/%d", index)
		operations = append(operations,
			map[string]any{"op": "test", "path": basePath + "/name", "value": update.Name},
			map[string]any{"op": "test", "path": basePath + "/image", "value": update.PreviousImage},
			map[string]any{"op": "replace", "path": basePath + "/image", "value": update.Image},
		)
		locations = append(locations, imageLocation{typeName: update.Type, name: update.Name, index: index})
	}
	patch, err := json.Marshal(operations)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build image patch: %w", err)
	}
	return patch, locations, nil
}

func (m *WorkloadManager) patchWorkloadImages(ctx context.Context, resolved *resolvedWorkloadImageTarget, patch []byte, locations []imageLocation, updates []WorkloadImageUpdate) (*unstructured.Unstructured, error) {
	ri := m.dynClient.Resource(resolved.targetGVR).Namespace(resolved.target.GetNamespace())
	updated, err := ri.Patch(ctx, resolved.targetName, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err == nil || !apierrors.IsInvalid(err) {
		if err != nil {
			return nil, fmt.Errorf("failed to update workload images: %w", err)
		}
		return updated, nil
	}

	latest, getErr := ri.Get(ctx, resolved.targetName, metav1.GetOptions{})
	if getErr != nil {
		return nil, fmt.Errorf("failed to re-read workload after rejected image patch: %w", getErr)
	}
	if !latest.GetDeletionTimestamp().IsZero() {
		return nil, fmt.Errorf("%s %s/%s: %w", latest.GetKind(), latest.GetNamespace(), latest.GetName(), ErrImageWorkloadTerminating)
	}
	retryPatch, retryLocations, classifyErr := buildImagePatch(latest, resolved.targetGVR.GroupResource(), resolved.templatePath, updates)
	if classifyErr != nil {
		return nil, classifyErr
	}
	if sameImageLocations(locations, retryLocations) {
		return nil, fmt.Errorf("failed to update workload images: %w", err)
	}

	updated, retryErr := ri.Patch(ctx, resolved.targetName, types.JSONPatchType, retryPatch, metav1.PatchOptions{})
	if retryErr != nil {
		if apierrors.IsInvalid(retryErr) {
			final, finalGetErr := ri.Get(ctx, resolved.targetName, metav1.GetOptions{})
			if finalGetErr != nil {
				return nil, fmt.Errorf("failed to re-read workload after retried image patch: %w", finalGetErr)
			}
			if !final.GetDeletionTimestamp().IsZero() {
				return nil, fmt.Errorf("%s %s/%s: %w", final.GetKind(), final.GetNamespace(), final.GetName(), ErrImageWorkloadTerminating)
			}
			if _, _, finalClassifyErr := buildImagePatch(final, resolved.targetGVR.GroupResource(), resolved.templatePath, updates); apierrors.IsConflict(finalClassifyErr) {
				return nil, finalClassifyErr
			}
		}
		return nil, fmt.Errorf("failed to update workload images: %w", retryErr)
	}
	return updated, nil
}

func sameImageLocations(left, right []imageLocation) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func imageConflict(resource schema.GroupResource, obj *unstructured.Unstructured, update WorkloadImageUpdate, currentImage string) error {
	return apierrors.NewConflict(
		resource,
		obj.GetName(),
		fmt.Errorf("image for %s %q changed from %q to %q; review the latest images before applying", update.Type, update.Name, update.PreviousImage, currentImage),
	)
}

func missingImageConflict(resource schema.GroupResource, obj *unstructured.Unstructured, update WorkloadImageUpdate) error {
	return apierrors.NewConflict(
		resource,
		obj.GetName(),
		fmt.Errorf("%s %q no longer exists; review the latest images before applying", update.Type, update.Name),
	)
}

func classifyWorkloadUpdateBehavior(normalizedKind string, root *unstructured.Unstructured) WorkloadUpdateBehavior {
	switch normalizedKind {
	case "deployments":
		paused, _, _ := unstructured.NestedBool(root.Object, "spec", "paused")
		if paused {
			return WorkloadUpdateBehavior{Type: "paused"}
		}
		strategy, _, _ := unstructured.NestedString(root.Object, "spec", "strategy", "type")
		if strategy == "Recreate" {
			return WorkloadUpdateBehavior{Type: "recreate"}
		}
		return WorkloadUpdateBehavior{Type: "rolling"}
	case "daemonsets":
		strategy, _, _ := unstructured.NestedString(root.Object, "spec", "updateStrategy", "type")
		if strategy == "OnDelete" {
			return WorkloadUpdateBehavior{Type: "onDelete"}
		}
		return WorkloadUpdateBehavior{Type: "rolling"}
	case "statefulsets":
		strategy, _, _ := unstructured.NestedString(root.Object, "spec", "updateStrategy", "type")
		if strategy == "OnDelete" {
			return WorkloadUpdateBehavior{Type: "onDelete"}
		}
		partition, found, _ := unstructured.NestedInt64(root.Object, "spec", "updateStrategy", "rollingUpdate", "partition")
		if found && partition > 0 {
			return WorkloadUpdateBehavior{Type: "partitioned", Partition: &partition}
		}
		return WorkloadUpdateBehavior{Type: "rolling"}
	case "rollouts":
		paused, _, _ := unstructured.NestedBool(root.Object, "spec", "paused")
		if paused {
			return WorkloadUpdateBehavior{Type: "paused"}
		}
		switch rollouts.StrategyOf(root) {
		case rollouts.StrategyCanary:
			steps, _, _ := unstructured.NestedSlice(root.Object, "spec", "strategy", "canary", "steps")
			gated := len(steps) > 0
			return WorkloadUpdateBehavior{Type: string(rollouts.StrategyCanary), Gated: &gated}
		case rollouts.StrategyBlueGreen:
			autoPromote, found, _ := unstructured.NestedBool(root.Object, "spec", "strategy", "blueGreen", "autoPromotionEnabled")
			if !found {
				autoPromote = true
			}
			return WorkloadUpdateBehavior{Type: string(rollouts.StrategyBlueGreen), AutoPromote: &autoPromote}
		}
		return WorkloadUpdateBehavior{Type: "rolling"}
	default:
		return WorkloadUpdateBehavior{Type: "rolling"}
	}
}

func WorkloadImageTargetForRollout(ro *unstructured.Unstructured) (group, resource string, needsGet bool, supported bool) {
	target, err := rollouts.ResolveTemplateTarget(ro)
	if err != nil {
		return "", "", false, false
	}
	return target.GVR.Group, target.GVR.Resource, target.GVR != rollouts.GVR || target.Name != ro.GetName(), true
}
