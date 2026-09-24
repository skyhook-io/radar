package k8score

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	// defaultDrainTimeout bounds a drain that only admits evictions.
	defaultDrainTimeout = 60 * time.Second
	// defaultDrainWaitTimeout bounds a drain that also waits for the evicted pods to
	// disappear. Admitting an eviction is quick; a pod then runs through its termination
	// grace period (30s by default, often minutes), so the wait needs more headroom.
	defaultDrainWaitTimeout = 2 * time.Minute
)

// drainWaitPollInterval is how often the wait phase re-lists the node's pods to see which
// evicted pods are gone. A package var so tests can shorten it.
var drainWaitPollInterval = 2 * time.Second

// DrainOptions configures a node drain operation.
type DrainOptions struct {
	IgnoreDaemonSets   bool          // Skip DaemonSet-managed pods (default should be true)
	DeleteEmptyDirData bool          // Allow draining pods that use emptyDir volumes
	Force              bool          // Evict pods not managed by a controller
	GracePeriodSeconds *int64        // Override pod termination grace period
	Timeout            time.Duration // Overall deadline for the drain (evictions and, if enabled, the wait)
	// WaitForDeletion makes DrainNode wait for the evicted pods to actually disappear before
	// returning, matching kubectl drain. Without it, DrainNode returns as soon as the Eviction
	// API has accepted every eviction, which does not mean the pods have finished terminating.
	WaitForDeletion bool
}

// DrainResult reports what happened during a drain operation.
type DrainResult struct {
	EvictedPods []string           `json:"evictedPods"`
	SkippedPods []PodDrainDecision `json:"skippedPods,omitempty"` // pods left alone and why
	Errors      []string           `json:"errors,omitempty"`
	// WaitedForDeletion is true when the drain waited for the evicted pods to actually be
	// deleted. When false, EvictedPods lists pods whose eviction was merely accepted and
	// which may still be terminating.
	WaitedForDeletion bool `json:"waitedForDeletion"`
	// PendingPods are evicted pods still present when the drain deadline passed — their
	// termination grace period had not finished. Only populated when the drain waited;
	// an empty list after a wait means every evicted pod is gone.
	PendingPods []string `json:"pendingPods,omitempty"`
}

// drainedPod identifies a pod whose eviction was accepted, so the wait phase can tell an
// evicted pod that is gone from a namesake recreated under a new UID.
type drainedPod struct {
	namespace string
	name      string
	uid       types.UID
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

// drainTimeout resolves the overall drain deadline, honouring an explicit Timeout and
// otherwise choosing a default sized for what the drain does: admitting evictions is quick,
// waiting for pods to finish terminating is not.
func drainTimeout(opts DrainOptions) time.Duration {
	switch {
	case opts.Timeout != 0:
		return opts.Timeout
	case opts.WaitForDeletion:
		return defaultDrainWaitTimeout
	default:
		return defaultDrainTimeout
	}
}

// DrainNode cordons the node and evicts all eligible pods.
func DrainNode(ctx context.Context, client kubernetes.Interface, nodeName string, opts DrainOptions) (*DrainResult, error) {
	opts.Timeout = drainTimeout(opts)

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

	// Decide per pod with the same classifier the drain plan uses. PDBs are not
	// pre-checked here: the Eviction API is authoritative and evictPod retries on 429.
	result := &DrainResult{}
	var toEvict []corev1.Pod
	for _, pod := range podList.Items {
		decision := ClassifyPodForDrain(pod, opts, nil)
		if decision.Outcome == DrainOutcomeSkip {
			result.SkippedPods = append(result.SkippedPods, decision)
			continue
		}
		toEvict = append(toEvict, pod)
	}
	logSkippedPods(nodeName, result.SkippedPods)

	if len(toEvict) == 0 {
		return result, nil
	}

	drainCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	evicted := evictPods(drainCtx, client, toEvict, opts.GracePeriodSeconds, result)

	// An accepted eviction is not a departed pod: the kubelet still runs it through its
	// termination grace period. Without this wait a caller that reboots on "drained"
	// SIGKILLs pods mid-shutdown, the exact thing draining is meant to avoid.
	if opts.WaitForDeletion && len(evicted) > 0 {
		result.WaitedForDeletion = true
		result.PendingPods = pendingPodNames(waitForPodsGone(drainCtx, client, nodeName, evicted))
	}

	return result, nil
}

// logSkippedPods records the pods a drain left alone, if any.
func logSkippedPods(nodeName string, skipped []PodDrainDecision) {
	if len(skipped) == 0 {
		return
	}
	names := make([]string, 0, len(skipped))
	for _, d := range skipped {
		names = append(names, d.Namespace+"/"+d.Name)
	}
	log.Printf("[node-ops] Drain %s: skipping %d pods: %v", nodeName, len(names), names)
}

// evictPods evicts every pod in toEvict with bounded concurrency, recording each outcome on
// result, and returns the pods whose eviction was accepted so the caller can wait them out.
func evictPods(ctx context.Context, client kubernetes.Interface, toEvict []corev1.Pod, gracePeriod *int64, result *DrainResult) []drainedPod {
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var mu sync.Mutex
	var wg sync.WaitGroup

	var evicted []drainedPod
	for i := range toEvict {
		pod := toEvict[i]
		wg.Add(1)
		go func() {
			defer wg.Done()

			err := evictPod(ctx, client, pod, gracePeriod, sem)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s/%s: %v", pod.Namespace, pod.Name, err))
			} else {
				result.EvictedPods = append(result.EvictedPods, pod.Namespace+"/"+pod.Name)
				evicted = append(evicted, drainedPod{namespace: pod.Namespace, name: pod.Name, uid: pod.UID})
			}
		}()
	}
	wg.Wait()
	return evicted
}

// pendingPodNames turns the pods still present after a wait into a sorted namespace/name list.
func pendingPodNames(pods []drainedPod) []string {
	if len(pods) == 0 {
		return nil
	}
	names := make([]string, 0, len(pods))
	for _, p := range pods {
		names = append(names, p.namespace+"/"+p.name)
	}
	sort.Strings(names)
	return names
}

// waitForPodsGone blocks until every pod in evicted has left the node or ctx is done, and
// returns those still present at that point. A pod counts as gone once it is absent from the
// node's pod list or has reappeared under a new UID (the old instance was deleted); a pod
// still present under its original UID is still inside its termination grace period.
func waitForPodsGone(ctx context.Context, client kubernetes.Interface, nodeName string, evicted []drainedPod) []drainedPod {
	remaining := make(map[types.UID]drainedPod, len(evicted))
	for _, p := range evicted {
		remaining[p.uid] = p
	}

	if pruneDepartedPods(ctx, client, nodeName, remaining) {
		return nil
	}

	ticker := time.NewTicker(drainWaitPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return mapValues(remaining)
		case <-ticker.C:
			if pruneDepartedPods(ctx, client, nodeName, remaining) {
				return nil
			}
		}
	}
}

// pruneDepartedPods drops from remaining every pod no longer present on the node and reports
// whether the set is now empty. A failed list leaves the set untouched, so a transient error
// costs one poll interval rather than a false "still here".
func pruneDepartedPods(ctx context.Context, client kubernetes.Interface, nodeName string, remaining map[types.UID]drainedPod) bool {
	list, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return false
	}
	present := make(map[types.UID]struct{}, len(list.Items))
	for i := range list.Items {
		present[list.Items[i].UID] = struct{}{}
	}
	for uid := range remaining {
		if _, ok := present[uid]; !ok {
			delete(remaining, uid)
		}
	}
	return len(remaining) == 0
}

// mapValues returns the values of m in unspecified order.
func mapValues(m map[types.UID]drainedPod) []drainedPod {
	out := make([]drainedPod, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	return out
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
// The concurrency slot is held only around each Eviction call, never across the
// backoff sleep: a handful of PDB-blocked pods sleeping in their retry loops would
// otherwise occupy every slot until the shared deadline, and pods with no budget at
// all would time out without a single eviction attempt.
func evictPod(ctx context.Context, client kubernetes.Interface, pod corev1.Pod, gracePeriod *int64, sem chan struct{}) error {
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
	attempted := false

	deadlineErr := func() error {
		if attempted {
			return fmt.Errorf("timed out waiting for PDB to allow eviction: %w", ctx.Err())
		}
		return fmt.Errorf("drain deadline passed before this pod could be attempted: %w", ctx.Err())
	}

	for {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return deadlineErr()
		}
		// The select above can pick the slot even when cancellation is also ready.
		if ctx.Err() != nil {
			<-sem
			return deadlineErr()
		}
		err := client.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction)
		<-sem
		attempted = true

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
