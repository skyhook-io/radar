package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

// WorkloadRevision represents a single revision in a workload's rollout history
type WorkloadRevision struct {
	Number    int64     `json:"number"`
	CreatedAt time.Time `json:"createdAt"`
	Image     string    `json:"image"`    // primary container image
	IsCurrent bool      `json:"isCurrent"`
	Replicas  int64     `json:"replicas"`
	Template  string    `json:"template,omitempty"` // Pod template spec as YAML (for revision diff)
}

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

// DeleteResourceOptions contains options for deleting a resource
type DeleteResourceOptions struct {
	Kind      string
	Namespace string
	Name      string
	Force     bool // Force delete with grace period 0
}

// DeleteResource deletes a Kubernetes resource
func DeleteResource(ctx context.Context, opts DeleteResourceOptions) error {
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	dynamicClient := GetDynamicClient()
	if dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	// Get GVR for this resource kind
	gvr, ok := discovery.GetGVR(opts.Kind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", opts.Kind)
	}

	// Force delete: strip finalizers first so the resource can actually be removed
	if opts.Force {
		finalizerPatch := []byte(`{"metadata":{"finalizers":null}}`)
		var patchErr error
		if opts.Namespace != "" {
			_, patchErr = dynamicClient.Resource(gvr).Namespace(opts.Namespace).Patch(ctx, opts.Name, types.MergePatchType, finalizerPatch, metav1.PatchOptions{})
		} else {
			_, patchErr = dynamicClient.Resource(gvr).Patch(ctx, opts.Name, types.MergePatchType, finalizerPatch, metav1.PatchOptions{})
		}
		if patchErr != nil && !apierrors.IsNotFound(patchErr) {
			if apierrors.IsForbidden(patchErr) {
				return fmt.Errorf("force delete requires patch permission to strip finalizers: %w", patchErr)
			}
			log.Printf("[delete] Failed to strip finalizers from %s %s/%s: %v", opts.Kind, opts.Namespace, opts.Name, patchErr)
		}
	}

	// Build delete options
	deleteOpts := metav1.DeleteOptions{}
	if opts.Force {
		gracePeriod := int64(0)
		deleteOpts.GracePeriodSeconds = &gracePeriod
	}

	// Delete the resource
	var err error
	if opts.Namespace != "" {
		err = dynamicClient.Resource(gvr).Namespace(opts.Namespace).Delete(ctx, opts.Name, deleteOpts)
	} else {
		err = dynamicClient.Resource(gvr).Delete(ctx, opts.Name, deleteOpts)
	}

	if err != nil {
		// Force delete may have already removed the resource (stripping finalizers
		// on a resource with deletionTimestamp causes immediate garbage collection)
		if opts.Force && apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete resource: %w", err)
	}

	// For non-force deletes, check if the resource is stuck (has finalizers blocking removal)
	if !opts.Force {
		var obj *unstructured.Unstructured
		if opts.Namespace != "" {
			obj, _ = dynamicClient.Resource(gvr).Namespace(opts.Namespace).Get(ctx, opts.Name, metav1.GetOptions{})
		} else {
			obj, _ = dynamicClient.Resource(gvr).Get(ctx, opts.Name, metav1.GetOptions{})
		}
		if obj != nil && obj.GetDeletionTimestamp() != nil && len(obj.GetFinalizers()) > 0 {
			return fmt.Errorf("resource is stuck in Terminating state due to finalizers — use force delete to remove it")
		}
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
		Object: map[string]any{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata": map[string]any{
				"name":      jobName,
				"namespace": namespace,
				"annotations": map[string]any{
					"cronjob.kubernetes.io/instantiate": "manual",
				},
				"ownerReferences": []any{
					map[string]any{
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

// ScaleWorkload scales a Deployment or StatefulSet to the specified replica count
func ScaleWorkload(ctx context.Context, kind, namespace, name string, replicas int32) error {
	dynamicClient := GetDynamicClient()
	if dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	discovery := GetResourceDiscovery()
	if discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	// Validate kind - only Deployments and StatefulSets support scaling
	normalizedKind := normalizeKind(kind)
	if normalizedKind != "deployments" && normalizedKind != "statefulsets" {
		return fmt.Errorf("scaling not supported for %s (only deployments and statefulsets)", kind)
	}

	// Get the GVR for the workload kind
	gvr, ok := discovery.GetGVR(normalizedKind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	// Patch the replica count
	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)

	_, err := dynamicClient.Resource(gvr).Namespace(namespace).Patch(
		ctx,
		name,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to scale workload: %w", err)
	}

	return nil
}

// normalizeKind converts various kind formats to the plural lowercase form
func normalizeKind(kind string) string {
	switch kind {
	case "Deployment", "deployment", "deployments":
		return "deployments"
	case "StatefulSet", "statefulset", "statefulsets":
		return "statefulsets"
	case "DaemonSet", "daemonset", "daemonsets":
		return "daemonsets"
	default:
		return kind
	}
}

// ListWorkloadRevisions returns the revision history for a Deployment, StatefulSet, or DaemonSet.
func ListWorkloadRevisions(ctx context.Context, kind, namespace, name string) ([]WorkloadRevision, error) {
	dynamicClient := GetDynamicClient()
	if dynamicClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	discovery := GetResourceDiscovery()
	if discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	normalizedKind := normalizeKind(kind)

	// First fetch the workload itself so we can match ownerReferences by UID
	workloadGVR, ok := discovery.GetGVR(normalizedKind)
	if !ok {
		return nil, fmt.Errorf("unknown resource kind: %s", kind)
	}

	workload, err := dynamicClient.Resource(workloadGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get workload: %w", err)
	}
	workloadUID := string(workload.GetUID())

	switch normalizedKind {
	case "deployments":
		return listDeploymentRevisions(ctx, discovery, namespace, name, workloadUID)
	case "statefulsets", "daemonsets":
		return listControllerRevisions(ctx, discovery, namespace, name, workloadUID)
	default:
		return nil, fmt.Errorf("revision history not supported for %s", kind)
	}
}

// listDeploymentRevisions lists revisions for a Deployment by finding its ReplicaSets.
func listDeploymentRevisions(ctx context.Context, discovery *ResourceDiscovery, namespace, name, workloadUID string) ([]WorkloadRevision, error) {
	dynamicClient := GetDynamicClient()

	rsGVR, ok := discovery.GetGVR("replicasets")
	if !ok {
		return nil, fmt.Errorf("replicasets resource not found")
	}

	rsList, err := dynamicClient.Resource(rsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list replicasets: %w", err)
	}

	var revisions []WorkloadRevision
	var maxRevision int64

	for _, rs := range rsList.Items {
		// Filter by ownerReference matching the Deployment UID
		owners := rs.GetOwnerReferences()
		owned := false
		for _, ref := range owners {
			if string(ref.UID) == workloadUID {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		// Extract revision number from annotation
		annotations := rs.GetAnnotations()
		revStr, ok := annotations["deployment.kubernetes.io/revision"]
		if !ok {
			continue
		}
		revNum, err := strconv.ParseInt(revStr, 10, 64)
		if err != nil {
			continue
		}

		// Extract primary container image
		image := extractContainerImage(rs.Object)

		// Extract replicas
		replicas, _, _ := unstructured.NestedInt64(rs.Object, "spec", "replicas")

		// Extract pod template as YAML for diff
		var templateStr string
		if template, found, _ := unstructured.NestedMap(rs.Object, "spec", "template"); found && template != nil {
			if templateYAML, err := yaml.Marshal(template); err == nil {
				templateStr = string(templateYAML)
			}
		}

		// Track the highest revision number
		if revNum > maxRevision {
			maxRevision = revNum
		}

		revisions = append(revisions, WorkloadRevision{
			Number:    revNum,
			CreatedAt: rs.GetCreationTimestamp().Time,
			Image:     image,
			Replicas:  replicas,
			Template:  templateStr,
		})
	}

	// Mark the current revision
	for i := range revisions {
		if revisions[i].Number == maxRevision {
			revisions[i].IsCurrent = true
		}
	}

	// Sort descending by revision number
	sort.Slice(revisions, func(i, j int) bool {
		return revisions[i].Number > revisions[j].Number
	})

	return revisions, nil
}

// listControllerRevisions lists revisions for a StatefulSet or DaemonSet by finding ControllerRevisions.
func listControllerRevisions(ctx context.Context, discovery *ResourceDiscovery, namespace, name, workloadUID string) ([]WorkloadRevision, error) {
	dynamicClient := GetDynamicClient()

	crGVR, ok := discovery.GetGVR("controllerrevisions")
	if !ok {
		return nil, fmt.Errorf("controllerrevisions resource not found")
	}

	crList, err := dynamicClient.Resource(crGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list controllerrevisions: %w", err)
	}

	var revisions []WorkloadRevision
	var maxRevision int64

	for _, cr := range crList.Items {
		// Filter by ownerReference matching the workload UID
		owners := cr.GetOwnerReferences()
		owned := false
		for _, ref := range owners {
			if string(ref.UID) == workloadUID {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		// Extract revision number from .revision field
		revNum, found, err := unstructured.NestedInt64(cr.Object, "revision")
		if err != nil || !found {
			continue
		}

		// Extract primary container image from .data.spec.template.spec.containers[0].image
		image := extractContainerImageFromData(cr.Object)

		// Extract template as YAML for diff
		// ControllerRevision .data contains the workload spec (includes template)
		var templateStr string
		if data, found, _ := unstructured.NestedMap(cr.Object, "data"); found && data != nil {
			// Try to extract just the pod template from the spec
			if template, tFound, _ := unstructured.NestedMap(data, "spec", "template"); tFound && template != nil {
				if templateYAML, err := yaml.Marshal(template); err == nil {
					templateStr = string(templateYAML)
				}
			} else {
				// Fallback: use the full data (some CRDs structure differently)
				if dataYAML, err := yaml.Marshal(data); err == nil {
					templateStr = string(dataYAML)
				}
			}
		}

		if revNum > maxRevision {
			maxRevision = revNum
		}

		revisions = append(revisions, WorkloadRevision{
			Number:    revNum,
			CreatedAt: cr.GetCreationTimestamp().Time,
			Image:     image,
			Template:  templateStr,
		})
	}

	// Mark the current revision
	for i := range revisions {
		if revisions[i].Number == maxRevision {
			revisions[i].IsCurrent = true
		}
	}

	// Sort descending by revision number
	sort.Slice(revisions, func(i, j int) bool {
		return revisions[i].Number > revisions[j].Number
	})

	return revisions, nil
}

// extractContainerImage extracts the first container image from spec.template.spec.containers[0].image
func extractContainerImage(obj map[string]any) string {
	containers, found, _ := unstructured.NestedSlice(obj, "spec", "template", "spec", "containers")
	if !found || len(containers) == 0 {
		return ""
	}
	if container, ok := containers[0].(map[string]any); ok {
		if image, ok := container["image"].(string); ok {
			return image
		}
	}
	return ""
}

// extractContainerImageFromData extracts the first container image from .data.spec.template.spec.containers[0].image
// for ControllerRevision objects.
func extractContainerImageFromData(obj map[string]any) string {
	data, found, _ := unstructured.NestedMap(obj, "data")
	if !found {
		return ""
	}
	return extractContainerImage(data)
}

// RollbackWorkload rolls back a Deployment, StatefulSet, or DaemonSet to a specific revision.
func RollbackWorkload(ctx context.Context, kind, namespace, name string, revision int64) error {
	dynamicClient := GetDynamicClient()
	if dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	discovery := GetResourceDiscovery()
	if discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	normalizedKind := normalizeKind(kind)

	// First fetch the workload to get its UID
	workloadGVR, ok := discovery.GetGVR(normalizedKind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	workload, err := dynamicClient.Resource(workloadGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get workload: %w", err)
	}
	workloadUID := string(workload.GetUID())

	switch normalizedKind {
	case "deployments":
		return rollbackDeployment(ctx, discovery, namespace, name, workloadUID, revision)
	case "statefulsets", "daemonsets":
		return rollbackControllerRevision(ctx, discovery, normalizedKind, namespace, name, workloadUID, revision)
	default:
		return fmt.Errorf("rollback not supported for %s", kind)
	}
}

// rollbackDeployment rolls back a Deployment by copying the target ReplicaSet's pod template.
func rollbackDeployment(ctx context.Context, discovery *ResourceDiscovery, namespace, name, workloadUID string, revision int64) error {
	dynamicClient := GetDynamicClient()

	rsGVR, ok := discovery.GetGVR("replicasets")
	if !ok {
		return fmt.Errorf("replicasets resource not found")
	}

	// List ReplicaSets to find the one with the target revision
	rsList, err := dynamicClient.Resource(rsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list replicasets: %w", err)
	}

	var targetRS *unstructured.Unstructured
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		owners := rs.GetOwnerReferences()
		owned := false
		for _, ref := range owners {
			if string(ref.UID) == workloadUID {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		annotations := rs.GetAnnotations()
		revStr, ok := annotations["deployment.kubernetes.io/revision"]
		if !ok {
			continue
		}
		revNum, err := strconv.ParseInt(revStr, 10, 64)
		if err != nil {
			continue
		}
		if revNum == revision {
			targetRS = rs
			break
		}
	}

	if targetRS == nil {
		return fmt.Errorf("revision %d not found", revision)
	}

	// Extract the pod template from the target RS
	podTemplate, found, err := unstructured.NestedMap(targetRS.Object, "spec", "template")
	if err != nil || !found {
		return fmt.Errorf("failed to extract pod template from revision %d", revision)
	}

	// Build the patch: replace the Deployment's spec.template with the target RS's template
	patchData := map[string]any{
		"spec": map[string]any{
			"template": podTemplate,
		},
	}
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to build rollback patch: %w", err)
	}

	deployGVR, ok := discovery.GetGVR("deployments")
	if !ok {
		return fmt.Errorf("deployments resource not found")
	}

	_, err = dynamicClient.Resource(deployGVR).Namespace(namespace).Patch(
		ctx,
		name,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to rollback deployment: %w", err)
	}

	return nil
}

// rollbackControllerRevision rolls back a StatefulSet or DaemonSet using a ControllerRevision's data.
func rollbackControllerRevision(ctx context.Context, discovery *ResourceDiscovery, normalizedKind, namespace, name, workloadUID string, revision int64) error {
	dynamicClient := GetDynamicClient()

	crGVR, ok := discovery.GetGVR("controllerrevisions")
	if !ok {
		return fmt.Errorf("controllerrevisions resource not found")
	}

	// List ControllerRevisions to find the one with the target revision
	crList, err := dynamicClient.Resource(crGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list controllerrevisions: %w", err)
	}

	var targetCR *unstructured.Unstructured
	for i := range crList.Items {
		cr := &crList.Items[i]
		owners := cr.GetOwnerReferences()
		owned := false
		for _, ref := range owners {
			if string(ref.UID) == workloadUID {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		revNum, found, err := unstructured.NestedInt64(cr.Object, "revision")
		if err != nil || !found {
			continue
		}
		if revNum == revision {
			targetCR = cr
			break
		}
	}

	if targetCR == nil {
		return fmt.Errorf("revision %d not found", revision)
	}

	// Extract the .data field from the ControllerRevision
	data, found, err := unstructured.NestedMap(targetCR.Object, "data")
	if err != nil || !found {
		return fmt.Errorf("failed to extract data from revision %d", revision)
	}

	// Build a strategic merge patch from the ControllerRevision's data
	patchBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to build rollback patch: %w", err)
	}

	gvr, ok := discovery.GetGVR(normalizedKind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", normalizedKind)
	}

	_, err = dynamicClient.Resource(gvr).Namespace(namespace).Patch(
		ctx,
		name,
		types.StrategicMergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to rollback workload: %w", err)
	}

	return nil
}
