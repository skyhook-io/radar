package k8score

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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

// WorkloadRevision represents a single revision in a workload's rollout history.
type WorkloadRevision struct {
	Number    int64     `json:"number"`
	CreatedAt time.Time `json:"createdAt"`
	Image     string    `json:"image"`    // primary container image
	IsCurrent bool      `json:"isCurrent"`
	Replicas  int64     `json:"replicas"`
	Template  string    `json:"template,omitempty"` // Pod template spec as YAML (for revision diff)
}

// UpdateResourceOptions contains options for updating a resource.
type UpdateResourceOptions struct {
	Kind      string
	Namespace string
	Name      string
	YAML      string // YAML content to apply
}

// DeleteResourceOptions contains options for deleting a resource.
type DeleteResourceOptions struct {
	Kind      string
	Namespace string
	Name      string
	Force     bool // Force delete with grace period 0
}

// WorkloadManager provides workload lifecycle operations using injected clients.
type WorkloadManager struct {
	dynClient dynamic.Interface
	discovery *ResourceDiscovery
}

// NewWorkloadManager creates a WorkloadManager with the given clients.
func NewWorkloadManager(dynClient dynamic.Interface, discovery *ResourceDiscovery) *WorkloadManager {
	return &WorkloadManager{dynClient: dynClient, discovery: discovery}
}

// UpdateResource updates a Kubernetes resource from YAML.
func (m *WorkloadManager) UpdateResource(ctx context.Context, opts UpdateResourceOptions) (*unstructured.Unstructured, error) {
	if m.discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}
	if m.dynClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(opts.YAML), &obj.Object); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	gvr, ok := m.discovery.GetGVR(opts.Kind)
	if !ok {
		return nil, fmt.Errorf("unknown resource kind: %s", opts.Kind)
	}

	if obj.GetName() != opts.Name {
		return nil, fmt.Errorf("resource name mismatch: expected %s, got %s", opts.Name, obj.GetName())
	}
	if opts.Namespace != "" && obj.GetNamespace() != opts.Namespace {
		return nil, fmt.Errorf("resource namespace mismatch: expected %s, got %s", opts.Namespace, obj.GetNamespace())
	}

	var result *unstructured.Unstructured
	var err error
	if opts.Namespace != "" {
		result, err = m.dynClient.Resource(gvr).Namespace(opts.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
	} else {
		result, err = m.dynClient.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update resource: %w", err)
	}

	return result, nil
}

// DeleteResource deletes a Kubernetes resource.
func (m *WorkloadManager) DeleteResource(ctx context.Context, opts DeleteResourceOptions) error {
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	gvr, ok := m.discovery.GetGVR(opts.Kind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", opts.Kind)
	}

	if opts.Force {
		finalizerPatch := []byte(`{"metadata":{"finalizers":null}}`)
		var patchErr error
		if opts.Namespace != "" {
			_, patchErr = m.dynClient.Resource(gvr).Namespace(opts.Namespace).Patch(ctx, opts.Name, types.MergePatchType, finalizerPatch, metav1.PatchOptions{})
		} else {
			_, patchErr = m.dynClient.Resource(gvr).Patch(ctx, opts.Name, types.MergePatchType, finalizerPatch, metav1.PatchOptions{})
		}
		if patchErr != nil && !apierrors.IsNotFound(patchErr) {
			if apierrors.IsForbidden(patchErr) {
				return fmt.Errorf("force delete requires patch permission to strip finalizers: %w", patchErr)
			}
			log.Printf("[delete] Failed to strip finalizers from %s %s/%s: %v", opts.Kind, opts.Namespace, opts.Name, patchErr)
		}
	}

	deleteOpts := metav1.DeleteOptions{}
	if opts.Force {
		gracePeriod := int64(0)
		deleteOpts.GracePeriodSeconds = &gracePeriod
	}

	var err error
	if opts.Namespace != "" {
		err = m.dynClient.Resource(gvr).Namespace(opts.Namespace).Delete(ctx, opts.Name, deleteOpts)
	} else {
		err = m.dynClient.Resource(gvr).Delete(ctx, opts.Name, deleteOpts)
	}
	if err != nil {
		if opts.Force && apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete resource: %w", err)
	}

	if !opts.Force {
		var obj *unstructured.Unstructured
		if opts.Namespace != "" {
			obj, _ = m.dynClient.Resource(gvr).Namespace(opts.Namespace).Get(ctx, opts.Name, metav1.GetOptions{})
		} else {
			obj, _ = m.dynClient.Resource(gvr).Get(ctx, opts.Name, metav1.GetOptions{})
		}
		if obj != nil && obj.GetDeletionTimestamp() != nil && len(obj.GetFinalizers()) > 0 {
			return fmt.Errorf("resource is stuck in Terminating state due to finalizers — use force delete to remove it")
		}
	}

	return nil
}

// TriggerCronJob creates a Job from a CronJob.
func (m *WorkloadManager) TriggerCronJob(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	if m.dynClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	cronJobGVR, ok := m.discovery.GetGVR("cronjobs")
	if !ok {
		return nil, fmt.Errorf("cronjobs resource not found")
	}

	cronJob, err := m.dynClient.Resource(cronJobGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get cronjob: %w", err)
	}

	jobTemplate, found, err := unstructured.NestedMap(cronJob.Object, "spec", "jobTemplate")
	if err != nil {
		return nil, fmt.Errorf("failed to get job template from cronjob %s/%s: %w", namespace, name, err)
	}
	if !found {
		return nil, fmt.Errorf("cronjob %s/%s has no spec.jobTemplate", namespace, name)
	}

	jobName := fmt.Sprintf("%s-manual-%d", name, time.Now().Unix())
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

	if spec, found, _ := unstructured.NestedMap(jobTemplate, "spec"); found {
		if err := unstructured.SetNestedMap(job.Object, spec, "spec"); err != nil {
			return nil, fmt.Errorf("failed to set job spec: %w", err)
		}
	}
	if labels, found, _ := unstructured.NestedStringMap(jobTemplate, "metadata", "labels"); found {
		if err := unstructured.SetNestedStringMap(job.Object, labels, "metadata", "labels"); err != nil {
			return nil, fmt.Errorf("failed to set job labels: %w", err)
		}
	}

	jobGVR, ok := m.discovery.GetGVR("jobs")
	if !ok {
		return nil, fmt.Errorf("jobs resource not found")
	}

	result, err := m.dynClient.Resource(jobGVR).Namespace(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	return result, nil
}

// SetCronJobSuspend sets the suspend field on a CronJob.
func (m *WorkloadManager) SetCronJobSuspend(ctx context.Context, namespace, name string, suspend bool) error {
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	cronJobGVR, ok := m.discovery.GetGVR("cronjobs")
	if !ok {
		return fmt.Errorf("cronjobs resource not found")
	}

	patch := fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend)
	_, err := m.dynClient.Resource(cronJobGVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to patch cronjob: %w", err)
	}

	return nil
}

// RestartWorkload performs a rolling restart on a Deployment, StatefulSet, or DaemonSet.
func (m *WorkloadManager) RestartWorkload(ctx context.Context, kind, namespace, name string) error {
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	gvr, ok := m.discovery.GetGVR(kind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	restartTime := time.Now().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, restartTime)

	_, err := m.dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to restart workload: %w", err)
	}

	return nil
}

// ScaleWorkload scales a Deployment or StatefulSet to the specified replica count.
func (m *WorkloadManager) ScaleWorkload(ctx context.Context, kind, namespace, name string, replicas int32) error {
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	normalizedKind := NormalizeWorkloadKind(kind)
	if normalizedKind != "deployments" && normalizedKind != "statefulsets" {
		return fmt.Errorf("scaling not supported for %s (only deployments and statefulsets)", kind)
	}

	gvr, ok := m.discovery.GetGVR(normalizedKind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	_, err := m.dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to scale workload: %w", err)
	}

	return nil
}

// ScaleWorkloadDirect scales a Deployment or StatefulSet without requiring a WorkloadManager.
// It accepts a dynamic.Interface directly, for use by callers that don't have a full WorkloadManager.
func ScaleWorkloadDirect(ctx context.Context, dynClient dynamic.Interface, kind, namespace, name string, replicas int32) error {
	if dynClient == nil {
		return fmt.Errorf("dynamic client must not be nil")
	}

	normalizedKind := NormalizeWorkloadKind(kind)

	var gvr schema.GroupVersionResource
	switch normalizedKind {
	case "deployments":
		gvr = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	case "statefulsets":
		gvr = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	default:
		return fmt.Errorf("scaling not supported for %s (only deployments and statefulsets)", kind)
	}

	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	_, err := dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to scale workload: %w", err)
	}

	return nil
}

// ListWorkloadRevisions returns the revision history for a Deployment, StatefulSet, or DaemonSet.
func (m *WorkloadManager) ListWorkloadRevisions(ctx context.Context, kind, namespace, name string) ([]WorkloadRevision, error) {
	if m.dynClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	normalizedKind := NormalizeWorkloadKind(kind)

	workloadGVR, ok := m.discovery.GetGVR(normalizedKind)
	if !ok {
		return nil, fmt.Errorf("unknown resource kind: %s", kind)
	}

	workload, err := m.dynClient.Resource(workloadGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get workload: %w", err)
	}
	workloadUID := string(workload.GetUID())

	switch normalizedKind {
	case "deployments":
		return m.listDeploymentRevisions(ctx, namespace, name, workloadUID)
	case "statefulsets", "daemonsets":
		return m.listControllerRevisions(ctx, namespace, name, workloadUID)
	default:
		return nil, fmt.Errorf("revision history not supported for %s", kind)
	}
}

func (m *WorkloadManager) listDeploymentRevisions(ctx context.Context, namespace, name, workloadUID string) ([]WorkloadRevision, error) {
	rsGVR, ok := m.discovery.GetGVR("replicasets")
	if !ok {
		return nil, fmt.Errorf("replicasets resource not found")
	}

	rsList, err := m.dynClient.Resource(rsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list replicasets: %w", err)
	}

	return BuildDeploymentRevisions(rsList.Items, workloadUID), nil
}

// BuildDeploymentRevisions builds WorkloadRevision[] from unstructured ReplicaSets owned by a Deployment.
// workloadUID filters by UID-based ownership; pass empty string to skip UID-based filter.
func BuildDeploymentRevisions(rsList []unstructured.Unstructured, workloadUID string) []WorkloadRevision {
	var revisions []WorkloadRevision
	var maxRevision int64

	for _, rs := range rsList {
		if workloadUID != "" {
			owned := false
			for _, ref := range rs.GetOwnerReferences() {
				if string(ref.UID) == workloadUID {
					owned = true
					break
				}
			}
			if !owned {
				continue
			}
		}

		revStr, ok := rs.GetAnnotations()["deployment.kubernetes.io/revision"]
		if !ok {
			continue
		}
		revNum, err := strconv.ParseInt(revStr, 10, 64)
		if err != nil {
			continue
		}

		image := extractContainerImage(rs.Object)
		replicas, _, _ := unstructured.NestedInt64(rs.Object, "spec", "replicas")

		var templateStr string
		if template, found, _ := unstructured.NestedMap(rs.Object, "spec", "template"); found && template != nil {
			if templateYAML, err := yaml.Marshal(template); err == nil {
				templateStr = string(templateYAML)
			}
		}

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

	for i := range revisions {
		if revisions[i].Number == maxRevision {
			revisions[i].IsCurrent = true
		}
	}

	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Number > revisions[j].Number })

	return revisions
}

func (m *WorkloadManager) listControllerRevisions(ctx context.Context, namespace, name, workloadUID string) ([]WorkloadRevision, error) {
	crGVR, ok := m.discovery.GetGVR("controllerrevisions")
	if !ok {
		return nil, fmt.Errorf("controllerrevisions resource not found")
	}

	crList, err := m.dynClient.Resource(crGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list controllerrevisions: %w", err)
	}

	return BuildControllerRevisions(crList.Items, workloadUID), nil
}

// BuildControllerRevisions builds WorkloadRevision[] from unstructured ControllerRevisions owned by a StatefulSet or DaemonSet.
// workloadUID filters by UID-based ownership; pass empty string to skip UID-based filter.
func BuildControllerRevisions(crList []unstructured.Unstructured, workloadUID string) []WorkloadRevision {
	var revisions []WorkloadRevision
	var maxRevision int64

	for _, cr := range crList {
		if workloadUID != "" {
			owned := false
			for _, ref := range cr.GetOwnerReferences() {
				if string(ref.UID) == workloadUID {
					owned = true
					break
				}
			}
			if !owned {
				continue
			}
		}

		revNum, found, err := unstructured.NestedInt64(cr.Object, "revision")
		if err != nil || !found {
			continue
		}

		image := extractContainerImageFromData(cr.Object)

		var templateStr string
		if data, found, _ := unstructured.NestedMap(cr.Object, "data"); found && data != nil {
			if template, tFound, _ := unstructured.NestedMap(data, "spec", "template"); tFound && template != nil {
				if templateYAML, err := yaml.Marshal(template); err == nil {
					templateStr = string(templateYAML)
				}
			} else {
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

	for i := range revisions {
		if revisions[i].Number == maxRevision {
			revisions[i].IsCurrent = true
		}
	}

	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Number > revisions[j].Number })

	return revisions
}

// RollbackWorkload rolls back a Deployment, StatefulSet, or DaemonSet to a specific revision.
func (m *WorkloadManager) RollbackWorkload(ctx context.Context, kind, namespace, name string, revision int64) error {
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	normalizedKind := NormalizeWorkloadKind(kind)

	workloadGVR, ok := m.discovery.GetGVR(normalizedKind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	workload, err := m.dynClient.Resource(workloadGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get workload: %w", err)
	}
	workloadUID := string(workload.GetUID())

	switch normalizedKind {
	case "deployments":
		return m.rollbackDeployment(ctx, namespace, name, workloadUID, revision)
	case "statefulsets", "daemonsets":
		return m.rollbackControllerRevision(ctx, normalizedKind, namespace, name, workloadUID, revision)
	default:
		return fmt.Errorf("rollback not supported for %s", kind)
	}
}

func (m *WorkloadManager) rollbackDeployment(ctx context.Context, namespace, name, workloadUID string, revision int64) error {
	rsGVR, ok := m.discovery.GetGVR("replicasets")
	if !ok {
		return fmt.Errorf("replicasets resource not found")
	}

	rsList, err := m.dynClient.Resource(rsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list replicasets: %w", err)
	}

	var targetRS *unstructured.Unstructured
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		owned := false
		for _, ref := range rs.GetOwnerReferences() {
			if string(ref.UID) == workloadUID {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		revStr, ok := rs.GetAnnotations()["deployment.kubernetes.io/revision"]
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

	podTemplate, found, err := unstructured.NestedMap(targetRS.Object, "spec", "template")
	if err != nil || !found {
		return fmt.Errorf("failed to extract pod template from revision %d", revision)
	}

	patchData := map[string]any{"spec": map[string]any{"template": podTemplate}}
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to build rollback patch: %w", err)
	}

	deployGVR, ok := m.discovery.GetGVR("deployments")
	if !ok {
		return fmt.Errorf("deployments resource not found")
	}

	_, err = m.dynClient.Resource(deployGVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to rollback deployment: %w", err)
	}

	return nil
}

func (m *WorkloadManager) rollbackControllerRevision(ctx context.Context, normalizedKind, namespace, name, workloadUID string, revision int64) error {
	crGVR, ok := m.discovery.GetGVR("controllerrevisions")
	if !ok {
		return fmt.Errorf("controllerrevisions resource not found")
	}

	crList, err := m.dynClient.Resource(crGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list controllerrevisions: %w", err)
	}

	var targetCR *unstructured.Unstructured
	for i := range crList.Items {
		cr := &crList.Items[i]
		owned := false
		for _, ref := range cr.GetOwnerReferences() {
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

	data, found, err := unstructured.NestedMap(targetCR.Object, "data")
	if err != nil || !found {
		return fmt.Errorf("failed to extract data from revision %d", revision)
	}

	patchBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to build rollback patch: %w", err)
	}

	gvr, ok := m.discovery.GetGVR(normalizedKind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", normalizedKind)
	}

	_, err = m.dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to rollback workload: %w", err)
	}

	return nil
}

// NormalizeWorkloadKind converts various kind formats to the plural lowercase form.
func NormalizeWorkloadKind(kind string) string {
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

func extractContainerImageFromData(obj map[string]any) string {
	data, found, _ := unstructured.NestedMap(obj, "data")
	if !found {
		return ""
	}
	return extractContainerImage(data)
}
