package k8s

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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

// TriggerCronJob creates a Job from a CronJob
func TriggerCronJob(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	dynamicClient := GetDynamicClient()
	if dynamicClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	discovery := GetResourceDiscovery()
	if discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	// Get the CronJob
	cronJobGVR, ok := discovery.GetGVR("cronjobs")
	if !ok {
		return nil, fmt.Errorf("cronjobs resource not found")
	}

	cronJob, err := dynamicClient.Resource(cronJobGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get cronjob: %w", err)
	}

	// Extract the job template from the cronjob
	jobTemplate, found, err := unstructured.NestedMap(cronJob.Object, "spec", "jobTemplate")
	if err != nil || !found {
		return nil, fmt.Errorf("failed to get job template from cronjob: %w", err)
	}

	// Create a new Job from the template
	jobName := fmt.Sprintf("%s-manual-%d", name, time.Now().Unix())

	// Build the Job object
	job := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata": map[string]interface{}{
				"name":      jobName,
				"namespace": namespace,
				"annotations": map[string]interface{}{
					"cronjob.kubernetes.io/instantiate": "manual",
				},
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion":         cronJob.GetAPIVersion(),
						"kind":               cronJob.GetKind(),
						"name":               cronJob.GetName(),
						"uid":                string(cronJob.GetUID()),
						"controller":         true,
						"blockOwnerDeletion": true,
					},
				},
			},
		},
	}

	// Copy spec from job template
	if spec, found, _ := unstructured.NestedMap(jobTemplate, "spec"); found {
		if err := unstructured.SetNestedMap(job.Object, spec, "spec"); err != nil {
			return nil, fmt.Errorf("failed to set job spec: %w", err)
		}
	}

	// Copy metadata labels from job template
	if labels, found, _ := unstructured.NestedStringMap(jobTemplate, "metadata", "labels"); found {
		if err := unstructured.SetNestedStringMap(job.Object, labels, "metadata", "labels"); err != nil {
			return nil, fmt.Errorf("failed to set job labels: %w", err)
		}
	}

	// Get the Jobs GVR
	jobGVR, ok := discovery.GetGVR("jobs")
	if !ok {
		return nil, fmt.Errorf("jobs resource not found")
	}

	// Create the job
	result, err := dynamicClient.Resource(jobGVR).Namespace(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	return result, nil
}

// SetCronJobSuspend sets the suspend field on a CronJob
func SetCronJobSuspend(ctx context.Context, namespace, name string, suspend bool) error {
	dynamicClient := GetDynamicClient()
	if dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	discovery := GetResourceDiscovery()
	if discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	// Get the CronJob GVR
	cronJobGVR, ok := discovery.GetGVR("cronjobs")
	if !ok {
		return fmt.Errorf("cronjobs resource not found")
	}

	// Patch the CronJob to set suspend
	patch := fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend)
	_, err := dynamicClient.Resource(cronJobGVR).Namespace(namespace).Patch(
		ctx,
		name,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to patch cronjob: %w", err)
	}

	return nil
}

// RestartWorkload performs a rolling restart on a Deployment, StatefulSet, or DaemonSet
func RestartWorkload(ctx context.Context, kind, namespace, name string) error {
	dynamicClient := GetDynamicClient()
	if dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	discovery := GetResourceDiscovery()
	if discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	// Get the GVR for the workload kind
	gvr, ok := discovery.GetGVR(kind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	// Patch to trigger a rolling restart by updating an annotation
	restartTime := time.Now().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, restartTime)

	_, err := dynamicClient.Resource(gvr).Namespace(namespace).Patch(
		ctx,
		name,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to restart workload: %w", err)
	}

	return nil
}
