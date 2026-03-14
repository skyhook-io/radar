package k8score

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// DrainOptions configures a node drain operation.
type DrainOptions struct {
	IgnoreDaemonSets   bool          // Skip DaemonSet-managed pods (default should be true)
	DeleteEmptyDirData bool          // Allow draining pods that use emptyDir volumes
	Force              bool          // Evict pods not managed by a controller
	GracePeriodSeconds *int64        // Override pod termination grace period
	Timeout            time.Duration // How long to wait for evictions (default 60s)
}

// DrainResult reports what happened during a drain operation.
type DrainResult struct {
	EvictedPods []string `json:"evictedPods"`
	Errors      []string `json:"errors,omitempty"`
}

// CordonNode marks a node as unschedulable.
// Uses strategic merge patch (idempotent, no read-modify-write race).
func CordonNode(ctx context.Context, client kubernetes.Interface, nodeName string) error {
	patch := []byte(`{"spec":{"unschedulable":true}}`)
	_, err := client.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("cordon node: %w", err)
	}
	return nil
}

// UncordonNode marks a node as schedulable.
// Uses strategic merge patch (idempotent, no read-modify-write race).
func UncordonNode(ctx context.Context, client kubernetes.Interface, nodeName string) error {
	patch := []byte(`{"spec":{"unschedulable":null}}`)
	_, err := client.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("uncordon node: %w", err)
	}
	return nil
}

// DrainNode cordons the node and evicts all eligible pods.
func DrainNode(ctx context.Context, client kubernetes.Interface, nodeName string, opts DrainOptions) (*DrainResult, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}

	// Cordon first to prevent new pods from being scheduled
	if err := CordonNode(ctx, client, nodeName); err != nil {
		return nil, fmt.Errorf("cordon before drain: %w", err)
	}

	// List all pods on this node
	podList, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods on node: %w", err)
	}

	// Filter pods to evict
	var toEvict []corev1.Pod
	var skipped []string
	for _, pod := range podList.Items {
		if shouldSkipPod(pod, opts, &skipped) {
			continue
		}
		toEvict = append(toEvict, pod)
	}

	if len(skipped) > 0 {
		log.Printf("[node-ops] Drain %s: skipping %d pods: %v", nodeName, len(skipped), skipped)
	}

	result := &DrainResult{}

	if len(toEvict) == 0 {
		return result, nil
	}

	// Evict pods with bounded concurrency
	drainCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := range toEvict {
		pod := toEvict[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			err := evictPod(drainCtx, client, pod, opts.GracePeriodSeconds)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s/%s: %v", pod.Namespace, pod.Name, err))
			} else {
				result.EvictedPods = append(result.EvictedPods, pod.Namespace+"/"+pod.Name)
			}
		}()
	}

	wg.Wait()
	return result, nil
}

// shouldSkipPod returns true if the pod should not be evicted during drain.
func shouldSkipPod(pod corev1.Pod, opts DrainOptions, skipped *[]string) bool {
	// Skip completed/failed pods
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return true
	}

	// Skip mirror pods (static pods managed by kubelet)
	if _, isMirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; isMirror {
		*skipped = append(*skipped, pod.Namespace+"/"+pod.Name+" (mirror)")
		return true
	}

	// Skip DaemonSet pods
	if opts.IgnoreDaemonSets && isDaemonSetPod(pod) {
		*skipped = append(*skipped, pod.Namespace+"/"+pod.Name+" (daemonset)")
		return true
	}

	// Skip unmanaged pods unless Force
	if !opts.Force && !hasManagedOwner(pod) {
		*skipped = append(*skipped, pod.Namespace+"/"+pod.Name+" (unmanaged)")
		return true
	}

	// Skip pods with emptyDir unless DeleteEmptyDirData
	if !opts.DeleteEmptyDirData && hasLocalStorage(pod) {
		*skipped = append(*skipped, pod.Namespace+"/"+pod.Name+" (local-storage)")
		return true
	}

	return false
}

func isDaemonSetPod(pod corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

func hasManagedOwner(pod corev1.Pod) bool {
	return len(pod.OwnerReferences) > 0
}

func hasLocalStorage(pod corev1.Pod) bool {
	for _, vol := range pod.Spec.Volumes {
		if vol.EmptyDir != nil {
			return true
		}
	}
	return false
}

// evictPod evicts a single pod, retrying on PDB conflicts until the context deadline.
func evictPod(ctx context.Context, client kubernetes.Interface, pod corev1.Pod, gracePeriod *int64) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
	}
	if gracePeriod != nil {
		eviction.DeleteOptions = &metav1.DeleteOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	backoff := 500 * time.Millisecond
	maxBackoff := 5 * time.Second

	for {
		err := client.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction)
		if err == nil {
			return nil
		}

		// Pod already gone
		if apierrors.IsNotFound(err) {
			return nil
		}

		// PDB blocking eviction — retry with backoff
		if apierrors.IsTooManyRequests(err) {
			select {
			case <-ctx.Done():
				return fmt.Errorf("timed out waiting for PDB to allow eviction: %w", ctx.Err())
			case <-time.After(backoff):
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
		}

		return fmt.Errorf("evict: %w", err)
	}
}
