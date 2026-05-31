package issues

import "github.com/skyhook-io/radar/pkg/issuesapi"

// dedupePodSchedulingOverProblem drops the generic problem-source row for a
// Pod when the scheduling source emitted one for the same Pod. A pod stuck
// post-bind (ContainerCreating on a CNI/volume stall) trips both: DetectProblems
// flags it Pending>5m and DetectPostBindProblems names the actual blocker. The
// scheduling row is strictly richer, so it wins. (Bind-time unschedulable pods
// are already skipped in DetectProblems, so this only fires on the post-bind
// overlap.) A plain DetectProblems skip can't replace this — the problem
// threshold is 5m but the post-bind event window is 10m, so a pod stuck >10m
// would lose its only row.
func dedupePodSchedulingOverProblem(in []Issue) []Issue {
	schedPods := map[string]bool{}
	for _, i := range in {
		if i.Source == SourceScheduling && i.Kind == "Pod" {
			schedPods[i.Namespace+"/"+i.Name] = true
		}
	}
	if len(schedPods) == 0 {
		return in
	}
	out := in[:0]
	for _, i := range in {
		if i.Source == SourceProblem && i.Kind == "Pod" && schedPods[i.Namespace+"/"+i.Name] {
			continue
		}
		out = append(out, i)
	}
	return out
}

// subjectRef returns the issue's grouping subject — the topmost owner when one
// was resolved (member pods collapse under their workload), otherwise the
// resource itself. Mirrors enrichIdentity so dedup keys on the same subject the
// ID is built from.
func subjectRef(i Issue) Ref {
	if i.Owner.Kind != "" {
		return i.Owner
	}
	return Ref{Group: i.Group, Kind: i.Kind, Namespace: i.Namespace, Name: i.Name}
}

// childCategories are the specific, root-cause symptoms that, when present for a
// subject, make the parent workload-level rollup (workload_degraded /
// rollout_stalled) redundant. A degraded Deployment with crashlooping pods is
// ONE incident — the crashloop — not two; keeping both is the inverse of
// "50 pods = 1 row".
var childCategories = map[issuesapi.Category]bool{
	issuesapi.CategoryCrashLoop:           true,
	issuesapi.CategoryHighRestart:         true,
	issuesapi.CategoryImagePullFailed:     true,
	issuesapi.CategoryOOMKilled:           true,
	issuesapi.CategoryContainerWaiting:    true,
	issuesapi.CategoryInitContainerFailed: true,
	issuesapi.CategoryLivenessProbeFail:   true,
	issuesapi.CategoryReadinessFailed:     true,
	issuesapi.CategoryUnschedulable:       true,
	issuesapi.CategoryQuotaExceeded:       true,
	issuesapi.CategoryMissingConfigRef:    true,
	issuesapi.CategoryVolumeMountFailed:   true,
	issuesapi.CategoryPVCPending:          true,
}

// parentRollupCategories are the workload-level summaries that should be
// suppressed when a more-specific child symptom exists for the same subject.
var parentRollupCategories = map[issuesapi.Category]bool{
	issuesapi.CategoryWorkloadDegraded: true,
	issuesapi.CategoryRolloutStalled:   true,
}

// dedupeWorkloadDegradedOverChild drops the parent workload rollup row
// (workload_degraded / rollout_stalled) for a subject when a more-specific
// child symptom (crashloop, image_pull_failed, …) of AT LEAST the parent's
// severity was classified for the SAME subject. A degraded Deployment whose
// pods are crashlooping is one incident, not two rows; the child names the
// actual root cause, so it wins.
//
// The severity gate is load-bearing: a critical "0/N available" rollup whose
// only child symptom is a warning (e.g. pods stuck Pending → container_waiting)
// must NOT be suppressed, or dropping the parent would silently downgrade the
// incident critical→warning. So the parent survives when it is strictly more
// severe than every child for the subject, and (as before) when no specific
// child symptom exists at all — a real degraded-without-visible-cause case is
// never dropped.
//
// Keys on subjectRef (owner-collapsed identity) so a parent row emitted on the
// Deployment matches child rows emitted on its member Pods, which carry the
// Deployment as their owner. Mirrors dedupePodSchedulingOverProblem's
// "richer row wins for the same subject" shape.
func dedupeWorkloadDegradedOverChild(in []Issue) []Issue {
	// Per subject, the worst severity among its specific child-symptom rows.
	maxChildSev := map[string]int{}
	for _, i := range in {
		if childCategories[i.Category] {
			k := subjectKeyOf(subjectRef(i))
			if r := SeverityRank(i.Severity); r > maxChildSev[k] {
				maxChildSev[k] = r
			}
		}
	}
	if len(maxChildSev) == 0 {
		return in
	}
	out := in[:0]
	for _, i := range in {
		if parentRollupCategories[i.Category] {
			// Suppress only when a child at least as severe exists — never
			// downgrade a critical rollup to a warning child.
			if r, ok := maxChildSev[subjectKeyOf(subjectRef(i))]; ok && r >= SeverityRank(i.Severity) {
				continue
			}
		}
		out = append(out, i)
	}
	return out
}

// subjectKeyOf is the canonical string key for a subject Ref — the same
// group|kind|namespace|name key the ID hash and audit deep-links use, so dedup
// can't drift from grouping.
func subjectKeyOf(r Ref) string {
	return resourceKey(r.Group, r.Kind, r.Namespace, r.Name)
}
