package issues

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

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
// rollout_stalled / job_failed) redundant. A degraded Deployment with
// crashlooping pods is ONE incident — the crashloop — not two; keeping both is
// the inverse of "50 pods = 1 row".
//
// The admission-rejection categories belong here because a workload that can't
// create its pods reports ReplicaFailure → the rollup, while the scheduling
// source names the actual rejection (no Pod exists yet). They are emitted on the
// same owner subject via the same path as quota_exceeded, so they fold the same way.
var childCategories = map[issuesapi.Category]bool{
	issuesapi.CategoryCrashLoop:                true,
	issuesapi.CategoryHighRestart:              true,
	issuesapi.CategoryImagePullFailed:          true,
	issuesapi.CategoryOOMKilled:                true,
	issuesapi.CategoryContainerWaiting:         true,
	issuesapi.CategoryInitContainerFailed:      true,
	issuesapi.CategoryLivenessProbeFail:        true,
	issuesapi.CategoryReadinessFailed:          true,
	issuesapi.CategoryUnschedulable:            true,
	issuesapi.CategoryQuotaExceeded:            true,
	issuesapi.CategoryAdmissionWebhookBlocking: true,
	issuesapi.CategoryPodSecurityViolation:     true,
	issuesapi.CategoryRBACForbidden:            true,
	issuesapi.CategoryMissingConfigRef:         true,
	issuesapi.CategoryVolumeMountFailed:        true,
	issuesapi.CategoryPVCPending:               true,
}

// parentRollupCategories are the workload-level summaries that should be
// suppressed when a more-specific child symptom exists for the same subject.
//
// job_failed is a rollup too: a failed Job's pods resolve their top owner to the
// Job, so a BackoffLimitExceeded Job whose pods crashloop/OOM/can't-pull is one
// incident — the pod cause is the root. A DeadlineExceeded job (the controller
// killed a slow-but-not-crashing pod) has no qualifying child, so the severity
// gate keeps its row. cronjob_failed is deliberately NOT here: "stale" /
// "never-scheduled" means no Jobs were produced at all — an orthogonal failure
// with no symptom children to fold into (a failed child Job surfaces as
// job_failed on that Job, which already resolves to the CronJob subject).
var parentRollupCategories = map[issuesapi.Category]bool{
	issuesapi.CategoryWorkloadDegraded: true,
	issuesapi.CategoryRolloutStalled:   true,
	issuesapi.CategoryJobFailed:        true,
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
	// A suppressed parent may have the only timing evidence for a subject: pod
	// rows derive issue_timing solely from the owner's Available condition, but a
	// surge rollout can stall with Available still True. Record each dropped
	// parent's issue_timing and donate it to children of the same subject as
	// "owner_condition", since from the child's perspective the
	// evidence is workload-level. Disagreeing suppressed parents donate
	// nothing, mirroring the group-fold agreement rule.
	suppressedIssueTiming := map[string]string{}
	out := in[:0]
	for _, i := range in {
		if parentRollupCategories[i.Category] {
			// Suppress only when a child at least as severe exists — never
			// downgrade a critical rollup to a warning child.
			k := subjectKeyOf(subjectRef(i))
			if r, ok := maxChildSev[k]; ok && r >= SeverityRank(i.Severity) {
				if i.IssueTiming != "" {
					if prev, seen := suppressedIssueTiming[k]; seen && prev != i.IssueTiming {
						suppressedIssueTiming[k] = ""
					} else if !seen {
						suppressedIssueTiming[k] = i.IssueTiming
					}
				}
				continue
			}
		}
		out = append(out, i)
	}
	// Subjects with at least one at-creation row: an after-healthy donation
	// onto a sibling would contradict that direct evidence.
	hasAtCreationRow := map[string]bool{}
	for _, i := range out {
		if i.IssueTiming == "started_at_resource_creation" {
			hasAtCreationRow[subjectKeyOf(subjectRef(i))] = true
		}
	}
	for idx := range out {
		i := &out[idx]
		if i.IssueTiming != "" || !childCategories[i.Category] {
			continue
		}
		timing := suppressedIssueTiming[subjectKeyOf(subjectRef(*i))]
		if timing == "" {
			continue
		}
		if timing == "started_after_resource_was_healthy" {
			// Restart-cycling children flip the owner's Available condition on
			// every crash, so a parent verdict derived from it is flap-poisoned
			// for exactly these rows — the same reason the pod detector omits
			// timing for them. And if any sibling proved at-creation directly,
			// an after-healthy donation would contradict it.
			if i.RestartCount >= 3 || restartCycleCategories[i.Category] || hasAtCreationRow[subjectKeyOf(subjectRef(*i))] {
				continue
			}
		}
		i.IssueTiming = timing
		i.IssueTimingBasis = "owner_condition"
	}
	return out
}

// restartCycleCategories are symptom categories whose pods restart-cycle and
// therefore flap the owner's readiness-derived conditions.
var restartCycleCategories = map[issuesapi.Category]bool{
	issuesapi.CategoryCrashLoop:         true,
	issuesapi.CategoryHighRestart:       true,
	issuesapi.CategoryOOMKilled:         true,
	issuesapi.CategoryLivenessProbeFail: true,
}

// dedupeConditionOverMissingRef drops a CRD condition row when a structural
// missing-reference detector already emitted the same category for the same
// object. Controller status commonly echoes dangling refs (for example Gateway
// Route ResolvedRefs=False), but the missing-ref row names the exact broken
// Service/port and works before controller reconciliation, so it is the richer
// row.
func dedupeConditionOverMissingRef(in []Issue) []Issue {
	structural := map[string]bool{}
	for _, i := range in {
		if i.Source != SourceMissingRef {
			continue
		}
		structural[issueResourceCategoryKey(i)] = true
	}
	if len(structural) == 0 {
		return in
	}
	out := in[:0]
	for _, i := range in {
		if i.Source == SourceCondition && structural[issueResourceCategoryKey(i)] && isMissingRefEchoCondition(i) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// dedupePVCPendingOverMissingRef drops the generic phase-Pending PVC row when
// the missing-StorageClass detector emitted a row for the same PVC. Both
// classify as pvc_pending but carry different fingerprints (so distinct IDs);
// the missing-ref row names the actual broken reference and is the richer one.
func dedupePVCPendingOverMissingRef(in []Issue) []Issue {
	structural := map[string]bool{}
	for _, i := range in {
		if i.Source == SourceMissingRef && i.Kind == "PersistentVolumeClaim" && i.Category == issuesapi.CategoryPVCPending {
			structural[issueResourceCategoryKey(i)] = true
		}
	}
	if len(structural) == 0 {
		return in
	}
	out := in[:0]
	for _, i := range in {
		if i.Source == SourceProblem && i.Kind == "PersistentVolumeClaim" && i.Category == issuesapi.CategoryPVCPending && structural[issueResourceCategoryKey(i)] {
			continue
		}
		out = append(out, i)
	}
	return out
}

// missingConfigCausesWaiting are the by-name dangling references whose failure
// surfaces as a container stuck in Waiting (CreateContainerConfigError): the
// referenced ConfigMap/Secret/ServiceAccount/imagePullSecret doesn't exist, so
// the kubelet can't build the container config. "Missing PVC" is excluded — it
// blocks scheduling (unschedulable), not container creation.
var missingConfigCausesWaiting = map[string]bool{
	"Missing ConfigMap":       true,
	"Missing Secret":          true,
	"Missing ServiceAccount":  true,
	"Missing imagePullSecret": true,
}

// structuralRootOverSymptom drops a runtime SYMPTOM row for a resource when a
// structural root row — a by-name dangling reference the detector resolved —
// exists for the SAME resource. The structural row names the exact broken object
// and the concrete fix and is the reason the symptom exists, so it is the richer
// row and always wins. Keyed on the resource itself (not the owner subject):
// both rows describe the same Pod/HPA, emitted by different detectors.
//
// Two evidence properties of the dropped symptom are donated to the surviving
// root so the fold loses nothing the operator needs:
//   - Severity: the root is promoted to the highest severity among the symptoms
//     it absorbs, so folding can never lower the displayed incident severity (the
//     floor dedupeWorkloadDegradedOverChild enforces, here via promotion since the
//     root is the survivor). A by-name root is stamped from resource age and has
//     no timing of its own.
//   - Timing: the root inherits the symptom's issue_timing when it has none. The
//     symptom (e.g. an HPA cannot-scale derived from the ScalingActive condition)
//     can carry the only accurate "started after the resource was healthy" signal;
//     disagreeing symptoms donate nothing, mirroring the rollup pass.
func structuralRootOverSymptom(in []Issue, isRoot, isSymptom func(Issue) bool) []Issue {
	rootExists := map[string]bool{}
	for _, i := range in {
		if isRoot(i) {
			rootExists[issueResourceKey(i)] = true
		}
	}
	if len(rootExists) == 0 {
		return in
	}
	foldedSev := map[string]int{}
	foldedTiming := map[string]string{}
	foldedBasis := map[string]string{}
	timingConflict := map[string]bool{}
	out := in[:0]
	for _, i := range in {
		if isSymptom(i) && rootExists[issueResourceKey(i)] {
			k := issueResourceKey(i)
			if r := SeverityRank(i.Severity); r > foldedSev[k] {
				foldedSev[k] = r
			}
			if i.IssueTiming != "" && !timingConflict[k] {
				if prev, ok := foldedTiming[k]; ok && prev != i.IssueTiming {
					timingConflict[k] = true
				} else if !ok {
					foldedTiming[k] = i.IssueTiming
					foldedBasis[k] = i.IssueTimingBasis
				}
			}
			continue
		}
		out = append(out, i)
	}
	for idx := range out {
		i := &out[idx]
		if !isRoot(*i) {
			continue
		}
		k := issueResourceKey(*i)
		if r, ok := foldedSev[k]; ok && r > SeverityRank(i.Severity) {
			i.Severity = severityForRank(r)
		}
		if i.IssueTiming == "" && !timingConflict[k] {
			if t := foldedTiming[k]; t != "" {
				i.IssueTiming = t
				i.IssueTimingBasis = foldedBasis[k]
			}
		}
	}
	return out
}

// dedupeContainerWaitingOverMissingRef drops the generic container_waiting pod
// row when a missing ConfigMap/Secret/ServiceAccount/imagePullSecret was
// structurally detected for the same pod: the dangling ref IS why the container
// can't start, and the missing-ref row names the exact object + fix.
func dedupeContainerWaitingOverMissingRef(in []Issue) []Issue {
	return structuralRootOverSymptom(in,
		func(i Issue) bool {
			return i.Source == SourceMissingRef && i.Kind == "Pod" &&
				i.Category == issuesapi.CategoryMissingConfigRef && missingConfigCausesWaiting[i.Reason]
		},
		func(i Issue) bool {
			// Only the config-stage waiting reason is caused by a missing
			// CM/Secret/SA — a multi-container pod can carry a different waiting
			// reason (RunContainerError, ContainerCreating) from an unrelated
			// container, which must not be folded away.
			return i.Source == SourceProblem && i.Kind == "Pod" &&
				i.Category == issuesapi.CategoryContainerWaiting && i.Reason == "CreateContainerConfigError"
		})
}

// (No image_pull_failed → missing-imagePullSecret fold: a *missing* pull-secret
// object surfaces as CreateContainerConfigError, already covered by the
// container_waiting fold above. A pod showing ImagePullBackOff while a pull
// secret is missing is more often an unrelated failure — wrong tag, not-found,
// rate-limit — so folding every image_pull_failed under the missing secret would
// hide real, independent pull errors. Left out deliberately.)

// dedupeHPAOverMissingTarget drops the hpa_limited_or_failed row when the
// autoscaler's scaleTargetRef points at a workload that doesn't exist. When the
// target is missing, every HPA/KEDA condition (can't-scale, no-metrics) is
// downstream of that one fact — there is no target to scale or read metrics for —
// so the missing-ref row is the single root. (When the target EXISTS there is no
// missing-ref row, so a genuine maxed/metrics problem is never touched.)
func dedupeHPAOverMissingTarget(in []Issue) []Issue {
	isAutoscaler := func(kind string) bool {
		return kind == "HorizontalPodAutoscaler" || kind == "ScaledObject"
	}
	return structuralRootOverSymptom(in,
		func(i Issue) bool {
			return i.Source == SourceMissingRef && isAutoscaler(i.Kind) && i.Reason == "Missing scaleTargetRef"
		},
		func(i Issue) bool {
			return i.Category == issuesapi.CategoryHPALimitedOrFailed && isAutoscaler(i.Kind)
		})
}

func isMissingRefEchoCondition(i Issue) bool {
	if i.Group != "gateway.networking.k8s.io" {
		return false
	}
	switch i.Kind {
	case "HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute":
		return strings.HasPrefix(i.Reason, "ResolvedRefs:")
	default:
		return false
	}
}

func issueResourceCategoryKey(i Issue) string {
	return resourceKey(i.Group, i.Kind, i.Namespace, i.Name) + "\x00" + string(i.Category)
}

// issueResourceKey is the canonical key for the issue's OWN resource (not its
// owner subject) — used by same-resource cross-detector dedup where two
// detectors describe the same Pod/HPA.
func issueResourceKey(i Issue) string {
	return resourceKey(i.Group, i.Kind, i.Namespace, i.Name)
}

// severityForRank inverts SeverityRank for the two issue-layer severities.
func severityForRank(r int) Severity {
	if r >= 3 {
		return SeverityCritical
	}
	return SeverityWarning
}

// subjectKeyOf is the canonical string key for a subject Ref — the same
// group|kind|namespace|name key the ID hash and audit deep-links use, so dedup
// can't drift from grouping.
func subjectKeyOf(r Ref) string {
	return resourceKey(r.Group, r.Kind, r.Namespace, r.Name)
}
