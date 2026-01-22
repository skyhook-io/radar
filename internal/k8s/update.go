package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// UpdateResourceOptions contains options for updating a resource
type UpdateResourceOptions struct {
	Kind      string
	Namespace string
	Name      string
	YAML      string // YAML content to apply
}

// UpdateResource updates a Kubernetes resource from YAML
func UpdateResource(ctx context.Context, opts UpdateResourceOptions) (*unstructured.Unstructured, error) {
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	dynamicClient := GetDynamicClient()
	if dynamicClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	// Parse YAML into unstructured
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(opts.YAML), &obj.Object); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	// Get GVR for this resource kind
	gvr, ok := discovery.GetGVR(opts.Kind)
	if !ok {
		return nil, fmt.Errorf("unknown resource kind: %s", opts.Kind)
	}

	// Validate that the resource matches what we're trying to update
	objName := obj.GetName()
	objNamespace := obj.GetNamespace()
	if objName != opts.Name {
		return nil, fmt.Errorf("resource name mismatch: expected %s, got %s", opts.Name, objName)
	}
	if opts.Namespace != "" && objNamespace != opts.Namespace {
		return nil, fmt.Errorf("resource namespace mismatch: expected %s, got %s", opts.Namespace, objNamespace)
	}

	// Update the resource
	var result *unstructured.Unstructured
	var err error
	if opts.Namespace != "" {
		result, err = dynamicClient.Resource(gvr).Namespace(opts.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
	} else {
		result, err = dynamicClient.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to update resource: %w", err)
	}

	return result, nil
}

// DeleteResource deletes a Kubernetes resource
func DeleteResource(ctx context.Context, kind, namespace, name string) error {
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	dynamicClient := GetDynamicClient()
	if dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	// Get GVR for this resource kind
	gvr, ok := discovery.GetGVR(kind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	// Delete the resource
	var err error
	if namespace != "" {
		err = dynamicClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	} else {
		err = dynamicClient.Resource(gvr).Delete(ctx, name, metav1.DeleteOptions{})
	}

	if err != nil {
		return fmt.Errorf("failed to delete resource: %w", err)
	}

	return nil
}
