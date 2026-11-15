package health

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SubstrateWorkerPoolReading is how an Agent Substrate WorkerPool (ate.dev)
// reads. Its only condition, Resolved, sits per sandbox class in
// status.sandboxClasses[] and records the generation it was computed for.
type SubstrateWorkerPoolReading struct {
	Verdict Verdict
	// Unresolved is set when a class's spec does not resolve to a usable
	// SandboxConfig on the current generation.
	Unresolved bool
	// Critical: unresolved with workers wanted and none ready, so nothing can
	// host the pool's actors. Ready workers keep running on the last resolved
	// config.
	Critical bool
}

// SubstrateWorkerPool returns the health of an Agent Substrate WorkerPool.
func SubstrateWorkerPool(u *unstructured.Unstructured) Verdict {
	return ReadSubstrateWorkerPool(u).Verdict
}

// ReadSubstrateWorkerPool classifies a WorkerPool from its per-class Resolved
// conditions and its desired and ready workers. Mirrored by getWorkerPoolStatus
// in k8s-ui; testdata/substrate_workerpool.json pins both.
func ReadSubstrateWorkerPool(u *unstructured.Unstructured) SubstrateWorkerPoolReading {
	if u == nil {
		return SubstrateWorkerPoolReading{Verdict: Verdict{Level: LevelUnknown, Reason: "Not reported"}}
	}
	if u.GetDeletionTimestamp() != nil {
		return SubstrateWorkerPoolReading{Verdict: Verdict{Level: LevelDegraded, Reason: "Terminating"}}
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
			return SubstrateWorkerPoolReading{Verdict: Verdict{Level: LevelDegraded, Reason: reason}}
		}
		critical := hasDesired && desired > 0 && ready == 0
		level := LevelDegraded
		if critical {
			level = LevelUnhealthy
		}
		return SubstrateWorkerPoolReading{Verdict: Verdict{Level: level, Reason: reason}, Unresolved: true, Critical: critical}
	}

	switch {
	case hasDesired && desired == 0 && pods > 0:
		return SubstrateWorkerPoolReading{Verdict: Verdict{Level: LevelDegraded, Reason: "Scaling down"}}
	case hasDesired && desired == 0:
		return SubstrateWorkerPoolReading{Verdict: Verdict{Level: LevelNeutral, Reason: "Scaled to zero"}}
	case !hasStatus || len(status) == 0 || len(conds) == 0:
		return SubstrateWorkerPoolReading{Verdict: Verdict{Level: LevelDegraded, Reason: "Pending"}}
	}
	for _, r := range conds {
		if r.stale || r.status != "True" {
			return SubstrateWorkerPoolReading{Verdict: Verdict{Level: LevelDegraded, Reason: "Resolving"}}
		}
	}
	if hasDesired && ready >= desired {
		return SubstrateWorkerPoolReading{Verdict: Verdict{Level: LevelHealthy, Reason: "Ready"}}
	}
	return SubstrateWorkerPoolReading{Verdict: Verdict{Level: LevelDegraded, Reason: "Scaling"}}
}
