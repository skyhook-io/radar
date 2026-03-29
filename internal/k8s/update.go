package k8s

import (
	"context"
	"log"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// Re-export types from pkg/k8score for backward compatibility.
type WorkloadRevision = k8score.WorkloadRevision
type UpdateResourceOptions = k8score.UpdateResourceOptions
type DeleteResourceOptions = k8score.DeleteResourceOptions

func getWorkloadManager() *k8score.WorkloadManager {
	var disc *k8score.ResourceDiscovery
	if d := GetResourceDiscovery(); d != nil {
		disc = d.ResourceDiscovery
	} else {
		log.Printf("[k8s] Warning: resource discovery not initialized; workload operations will fail until cluster is ready")
	}
	return k8score.NewWorkloadManager(GetDynamicClient(), disc)
}

func getWorkloadManagerWithClient(client dynamic.Interface) *k8score.WorkloadManager {
	if client == nil {
		return getWorkloadManager()
	}
	var disc *k8score.ResourceDiscovery
	if d := GetResourceDiscovery(); d != nil {
		disc = d.ResourceDiscovery
	} else {
		log.Printf("[k8s] Warning: resource discovery not initialized; workload operations will fail until cluster is ready")
	}
	return k8score.NewWorkloadManager(client, disc)
}

// UpdateResource updates a Kubernetes resource from YAML.
func UpdateResource(ctx context.Context, opts UpdateResourceOptions) (*unstructured.Unstructured, error) {
	return getWorkloadManager().UpdateResource(ctx, opts)
}

// UpdateResourceWithClient updates a Kubernetes resource using the provided client.
// If client is nil, uses the shared dynamic client.
func UpdateResourceWithClient(ctx context.Context, opts UpdateResourceOptions, client dynamic.Interface) (*unstructured.Unstructured, error) {
	return getWorkloadManagerWithClient(client).UpdateResource(ctx, opts)
}

// DeleteResource deletes a Kubernetes resource.
func DeleteResource(ctx context.Context, opts DeleteResourceOptions) error {
	return getWorkloadManager().DeleteResource(ctx, opts)
}

// DeleteResourceWithClient deletes a Kubernetes resource using the provided client.
// If client is nil, uses the shared dynamic client.
func DeleteResourceWithClient(ctx context.Context, opts DeleteResourceOptions, client dynamic.Interface) error {
	return getWorkloadManagerWithClient(client).DeleteResource(ctx, opts)
}

// TriggerCronJob creates a Job from a CronJob.
func TriggerCronJob(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return getWorkloadManager().TriggerCronJob(ctx, namespace, name)
}

// TriggerCronJobWithClient creates a Job from a CronJob using the provided client.
func TriggerCronJobWithClient(ctx context.Context, namespace, name string, client dynamic.Interface) (*unstructured.Unstructured, error) {
	return getWorkloadManagerWithClient(client).TriggerCronJob(ctx, namespace, name)
}

// SetCronJobSuspend sets the suspend field on a CronJob.
func SetCronJobSuspend(ctx context.Context, namespace, name string, suspend bool) error {
	return getWorkloadManager().SetCronJobSuspend(ctx, namespace, name, suspend)
}

// SetCronJobSuspendWithClient sets the suspend field on a CronJob using the provided client.
func SetCronJobSuspendWithClient(ctx context.Context, namespace, name string, suspend bool, client dynamic.Interface) error {
	return getWorkloadManagerWithClient(client).SetCronJobSuspend(ctx, namespace, name, suspend)
}

// RestartWorkload performs a rolling restart on a Deployment, StatefulSet, or DaemonSet.
func RestartWorkload(ctx context.Context, kind, namespace, name string) error {
	return getWorkloadManager().RestartWorkload(ctx, kind, namespace, name)
}

// RestartWorkloadWithClient performs a rolling restart using the provided client.
func RestartWorkloadWithClient(ctx context.Context, kind, namespace, name string, client dynamic.Interface) error {
	return getWorkloadManagerWithClient(client).RestartWorkload(ctx, kind, namespace, name)
}

// ScaleWorkload scales a Deployment or StatefulSet to the specified replica count.
func ScaleWorkload(ctx context.Context, kind, namespace, name string, replicas int32) error {
	return getWorkloadManager().ScaleWorkload(ctx, kind, namespace, name, replicas)
}

// ScaleWorkloadWithClient scales a workload using the provided client.
func ScaleWorkloadWithClient(ctx context.Context, kind, namespace, name string, replicas int32, client dynamic.Interface) error {
	return getWorkloadManagerWithClient(client).ScaleWorkload(ctx, kind, namespace, name, replicas)
}

// ListWorkloadRevisions returns the revision history for a Deployment, StatefulSet, or DaemonSet.
func ListWorkloadRevisions(ctx context.Context, kind, namespace, name string) ([]WorkloadRevision, error) {
	return getWorkloadManager().ListWorkloadRevisions(ctx, kind, namespace, name)
}

// ListWorkloadRevisionsWithClient returns revision history using the provided client.
// Use this when auth is enabled to ensure K8s RBAC applies to the requesting user.
func ListWorkloadRevisionsWithClient(ctx context.Context, kind, namespace, name string, client dynamic.Interface) ([]WorkloadRevision, error) {
	return getWorkloadManagerWithClient(client).ListWorkloadRevisions(ctx, kind, namespace, name)
}

// RollbackWorkload rolls back a Deployment, StatefulSet, or DaemonSet to a specific revision.
func RollbackWorkload(ctx context.Context, kind, namespace, name string, revision int64) error {
	return getWorkloadManager().RollbackWorkload(ctx, kind, namespace, name, revision)
}

// RollbackWorkloadWithClient rolls back a workload using the provided client.
func RollbackWorkloadWithClient(ctx context.Context, kind, namespace, name string, revision int64, client dynamic.Interface) error {
	return getWorkloadManagerWithClient(client).RollbackWorkload(ctx, kind, namespace, name, revision)
}
