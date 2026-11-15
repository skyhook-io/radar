package health

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SubstrateWorkerPool returns the health of an Agent Substrate WorkerPool
// (ate.dev) from its per-class Resolved conditions and its desired and ready
// workers. Its only condition, Resolved, sits per sandbox class in
// status.sandboxClasses[] and records the generation it was computed for.
// Mirrored by getWorkerPoolStatus in k8s-ui; testdata/substrate_workerpool.json
// pins both.
func SubstrateWorkerPool(u *unstructured.Unstructured) Verdict {
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
	return Verdict{Level: LevelDegraded, Reason: "Scaling"}
}
