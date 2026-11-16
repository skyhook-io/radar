package health

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// WorkerPoolProvisionGrace covers the controller's next pass: it creates the
// worker Deployment after writing the resolved classes, and records the
// Deployment's selector in status only on the pass after that.
const WorkerPoolProvisionGrace = 10 * time.Minute

// SubstrateWorkerPool returns the health of an Agent Substrate WorkerPool
// (ate.dev) from its per-class Resolved conditions and its desired and ready
// workers. Its only condition, Resolved, sits per sandbox class in
// status.sandboxClasses[] and records the generation it was computed for.
// Mirrored by getWorkerPoolStatus in k8s-ui; testdata/substrate_workerpool.json
// pins both.
func SubstrateWorkerPool(u *unstructured.Unstructured) Verdict {
	return SubstrateWorkerPoolAt(u, time.Now())
}

// SubstrateWorkerPoolAt is SubstrateWorkerPool judged at now.
func SubstrateWorkerPoolAt(u *unstructured.Unstructured, now time.Time) Verdict {
	if u == nil {
		return Verdict{Level: LevelUnknown, Reason: "Not reported"}
	}
	if u.GetDeletionTimestamp() != nil {
		return Verdict{Level: LevelDegraded, Reason: "Terminating"}
	}
	status, hasStatus, _ := unstructured.NestedMap(u.Object, "status")
	classes, _, _ := unstructured.NestedSlice(u.Object, "status", "sandboxClasses")
	desired, hasDesired, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	pods, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
	generation := u.GetGeneration()

	type resolved struct {
		status, reason string
		stale, found   bool
	}
	conds := make([]resolved, 0, len(classes))
	for _, raw := range classes {
		class, _ := raw.(map[string]any)
		r := resolved{}
		list, _ := class["conditions"].([]any)
		for _, rc := range list {
			cond, _ := rc.(map[string]any)
			if cond["type"] != "Resolved" {
				continue
			}
			r.found = true
			r.status, _ = cond["status"].(string)
			r.reason, _ = cond["reason"].(string)
			if observed, ok, _ := unstructured.NestedInt64(cond, "observedGeneration"); ok && observed > 0 && generation > 0 && observed < generation {
				r.stale = true
			}
		}
		conds = append(conds, r)
	}

	for _, r := range conds {
		if r.status != "False" {
			continue
		}
		reason := r.reason
		if reason == "" {
			reason = "Unresolved"
		}
		// Reported for an older spec: the controller has not judged the current one.
		if r.stale {
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
	case !hasStatus || len(status) == 0 || len(conds) == 0:
		return Verdict{Level: LevelDegraded, Reason: "Pending"}
	}
	for _, r := range conds {
		if r.stale || r.status != "True" {
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
// WorkerPoolProvisionGrace, with the time the last class resolved (or the
// pool's creation). The controller only logs a Deployment or ServiceAccount
// create the API server rejects, so an empty status.selector with no worker
// Pods is the only trace it leaves.
func SubstrateWorkerPoolStalled(u *unstructured.Unstructured, now time.Time) (time.Time, bool) {
	if u == nil || u.GetDeletionTimestamp() != nil {
		return time.Time{}, false
	}
	desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	selector, _, _ := unstructured.NestedString(u.Object, "status", "selector")
	pods, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
	classes, _, _ := unstructured.NestedSlice(u.Object, "status", "sandboxClasses")
	if desired <= 0 || selector != "" || pods > 0 || len(classes) == 0 {
		return time.Time{}, false
	}
	since := u.GetCreationTimestamp().Time
	generation := u.GetGeneration()
	for _, raw := range classes {
		class, _ := raw.(map[string]any)
		conds, _ := class["conditions"].([]any)
		resolved := false
		for _, rawCond := range conds {
			cond, _ := rawCond.(map[string]any)
			if cond["type"] != "Resolved" || cond["status"] != "True" {
				continue
			}
			if observed, ok, _ := unstructured.NestedInt64(cond, "observedGeneration"); ok && generation > 0 && observed > 0 && observed < generation {
				continue
			}
			resolved = true
			if ts, _ := cond["lastTransitionTime"].(string); ts != "" {
				if at, err := time.Parse(time.RFC3339, ts); err == nil && at.After(since) {
					since = at
				}
			}
		}
		if !resolved {
			return time.Time{}, false
		}
	}
	if since.IsZero() || now.Sub(since) < WorkerPoolProvisionGrace {
		return time.Time{}, false
	}
	return since, true
}
