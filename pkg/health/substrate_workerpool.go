package health

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// workerPoolProvisionGrace covers the controller's next pass: it creates the
// worker Deployment after writing the resolved classes, and records the
// Deployment's selector in status only on the pass after that.
const workerPoolProvisionGrace = 10 * time.Minute

// WorkerPoolClass is one sandbox class of a WorkerPool and its Resolved
// condition, the pool's only condition.
type WorkerPoolClass struct {
	Name string
	// Status is empty when the class has no Resolved condition yet.
	Status, Reason, Message string
	ObservedGeneration      int64
	// Stale: recorded for an older generation, so the controller has not
	// judged the current spec yet.
	Stale        bool
	TransitionAt time.Time // zero when unrecorded
}

// WorkerPoolClasses reads status.sandboxClasses[] and each class's Resolved
// condition. Mirrored by workerPoolResolvedConditions in k8s-ui.
func WorkerPoolClasses(u *unstructured.Unstructured) []WorkerPoolClass {
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "sandboxClasses")
	generation := u.GetGeneration()
	out := make([]WorkerPoolClass, 0, len(raw))
	for _, rc := range raw {
		class, _ := rc.(map[string]any)
		c := WorkerPoolClass{}
		c.Name, _ = class["name"].(string)
		list, _ := class["conditions"].([]any)
		for _, rawCond := range list {
			cond, _ := rawCond.(map[string]any)
			if cond["type"] != "Resolved" {
				continue
			}
			c.Status, _ = cond["status"].(string)
			c.Reason, _ = cond["reason"].(string)
			c.Message, _ = cond["message"].(string)
			c.ObservedGeneration, _, _ = unstructured.NestedInt64(cond, "observedGeneration")
			c.Stale = converging(generation, c.ObservedGeneration)
			if ts, _ := cond["lastTransitionTime"].(string); ts != "" {
				if at, err := time.Parse(time.RFC3339, ts); err == nil {
					c.TransitionAt = at
				}
			}
			break
		}
		out = append(out, c)
	}
	return out
}

// SubstrateWorkerPool returns the health of an Agent Substrate WorkerPool
// (ate.dev) from its per-class Resolved conditions and its desired and ready
// workers. Its only condition, Resolved, sits per sandbox class in
// status.sandboxClasses[] and records the generation it was computed for.
// Mirrored by getWorkerPoolStatus in k8s-ui; testdata/substrate_workerpool.json
// pins both.
func SubstrateWorkerPool(u *unstructured.Unstructured, now time.Time) Verdict {
	if u == nil {
		return Verdict{Level: LevelUnknown, Reason: "Not reported"}
	}
	if u.GetDeletionTimestamp() != nil {
		return Verdict{Level: LevelDegraded, Reason: "Terminating"}
	}
	status, hasStatus, _ := unstructured.NestedMap(u.Object, "status")
	desired, hasDesired, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	pods, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
	classes := WorkerPoolClasses(u)

	for _, c := range classes {
		if c.Status != "False" {
			continue
		}
		reason := c.Reason
		if reason == "" {
			reason = "Unresolved"
		}
		// Reported for an older spec: the controller has not judged the current one.
		if c.Stale {
			return Verdict{Level: LevelDegraded, Reason: reason}
		}
		// With no worker ready, nothing can host the pool's actors. Ready
		// workers keep running on the last resolved config.
		level := LevelDegraded
		if hasDesired && desired > 0 && ready == 0 {
			level = LevelUnhealthy
		}
		return Verdict{Level: level, Reason: reason}
	}

	switch {
	case hasDesired && desired == 0 && pods > 0:
		return Verdict{Level: LevelDegraded, Reason: "Scaling down"}
	case hasDesired && desired == 0:
		return Verdict{Level: LevelNeutral, Reason: "Scaled to zero"}
	case !hasStatus || len(status) == 0 || len(classes) == 0:
		return Verdict{Level: LevelDegraded, Reason: "Pending"}
	}
	for _, c := range classes {
		if c.Stale || c.Status != "True" {
			return Verdict{Level: LevelDegraded, Reason: "Resolving"}
		}
	}
	if hasDesired && ready >= desired {
		return Verdict{Level: LevelHealthy, Reason: "Ready"}
	}
	if _, stalled := SubstrateWorkerPoolStalled(u, now); stalled {
		return Verdict{Level: LevelUnhealthy, Reason: "Provisioning stalled"}
	}
	return Verdict{Level: LevelDegraded, Reason: "Scaling"}
}

// SubstrateWorkerPoolStalled reports a pool that wants workers, has every class
// resolved on its current generation, and still has no worker Deployment past
// workerPoolProvisionGrace, with the time the last class resolved (or the
// pool's creation). The controller only logs a Deployment or ServiceAccount create the
// API server rejects, so an empty status.selector with no worker Pods is the
// only trace it leaves.
func SubstrateWorkerPoolStalled(u *unstructured.Unstructured, now time.Time) (time.Time, bool) {
	if u == nil || u.GetDeletionTimestamp() != nil {
		return time.Time{}, false
	}
	desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	selector, _, _ := unstructured.NestedString(u.Object, "status", "selector")
	pods, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
	classes := WorkerPoolClasses(u)
	if desired <= 0 || selector != "" || pods > 0 || len(classes) == 0 {
		return time.Time{}, false
	}
	since := u.GetCreationTimestamp().Time
	for _, c := range classes {
		if c.Status != "True" || c.Stale {
			return time.Time{}, false
		}
		if c.TransitionAt.After(since) {
			since = c.TransitionAt
		}
	}
	if since.IsZero() || now.Sub(since) < workerPoolProvisionGrace {
		return time.Time{}, false
	}
	return since, true
}
