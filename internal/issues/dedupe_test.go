package issues

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestDedupePodSchedulingOverProblem(t *testing.T) {
	sched := Issue{Source: SourceScheduling, Kind: "Pod", Namespace: "ns", Name: "web-abc"}
	problemSamePod := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "web-abc"}
	problemOtherPod := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "api-xyz"}

	t.Run("drops problem row when scheduling row covers the same pod", func(t *testing.T) {
		out := dedupePodSchedulingOverProblem([]Issue{sched, problemSamePod})
		if len(out) != 1 || out[0].Source != SourceScheduling {
			t.Fatalf("expected only the scheduling row to survive, got %+v", out)
		}
	})

	// The >10m stuck-pod case the doc comment guards: a problem-source row with
	// no scheduling counterpart is the pod's only row and must NOT be dropped.
	t.Run("keeps problem row with no scheduling counterpart", func(t *testing.T) {
		out := dedupePodSchedulingOverProblem([]Issue{sched, problemOtherPod})
		var keptOther bool
		for _, i := range out {
			if i.Name == "api-xyz" {
				keptOther = true
			}
		}
		if !keptOther {
			t.Fatalf("expected the uncovered problem row to survive, got %+v", out)
		}
	})

	t.Run("no scheduling rows is a no-op", func(t *testing.T) {
		in := []Issue{problemSamePod, problemOtherPod}
		out := dedupePodSchedulingOverProblem(in)
		if len(out) != 2 {
			t.Fatalf("expected both rows to survive when no scheduling row exists, got %+v", out)
		}
	})
}

func TestDedupeWorkloadDegradedOverChild_Phase0(t *testing.T) {
	dep := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}

	hasCategory := func(out []Issue, c issuesapi.Category) bool {
		for _, i := range out {
			if i.Category == c {
				return true
			}
		}
		return false
	}

	t.Run("job_failed folds into crashlooping child pod", func(t *testing.T) {
		job := Ref{Group: "batch", Kind: "Job", Namespace: "ns", Name: "import"}
		jobFailed := Issue{Source: SourceProblem, Group: "batch", Kind: "Job", Namespace: "ns", Name: "import",
			Category: issuesapi.CategoryJobFailed, Severity: SeverityCritical, Reason: "BackoffLimitExceeded"}
		childCrash := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "import-xyz",
			Owner: job, Category: issuesapi.CategoryCrashLoop, Severity: SeverityCritical}
		out := dedupeWorkloadDegradedOverChild([]Issue{jobFailed, childCrash})
		if hasCategory(out, issuesapi.CategoryJobFailed) {
			t.Fatalf("job_failed rollup should fold into the crashloop child, got %+v", out)
		}
		if !hasCategory(out, issuesapi.CategoryCrashLoop) {
			t.Fatalf("crashloop child should survive as the root cause, got %+v", out)
		}
	})

	t.Run("job_failed survives DeadlineExceeded with no crash child", func(t *testing.T) {
		jobFailed := Issue{Source: SourceProblem, Group: "batch", Kind: "Job", Namespace: "ns", Name: "slow",
			Category: issuesapi.CategoryJobFailed, Severity: SeverityCritical, Reason: "DeadlineExceeded"}
		out := dedupeWorkloadDegradedOverChild([]Issue{jobFailed})
		if !hasCategory(out, issuesapi.CategoryJobFailed) {
			t.Fatalf("DeadlineExceeded job_failed with no child must survive, got %+v", out)
		}
	})

	t.Run("rollout_stalled folds into admission rejection on same owner", func(t *testing.T) {
		rollout := Issue{Source: SourceProblem, Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web",
			Category: issuesapi.CategoryRolloutStalled, Severity: SeverityCritical, Reason: "ReplicaFailure"}
		admission := Issue{Source: SourceScheduling, Group: "apps", Kind: "ReplicaSet", Namespace: "ns", Name: "web-abc",
			Owner: dep, Category: issuesapi.CategoryAdmissionWebhookBlocking, Severity: SeverityCritical}
		out := dedupeWorkloadDegradedOverChild([]Issue{rollout, admission})
		if hasCategory(out, issuesapi.CategoryRolloutStalled) {
			t.Fatalf("rollout_stalled should fold into the admission rejection root, got %+v", out)
		}
		if !hasCategory(out, issuesapi.CategoryAdmissionWebhookBlocking) {
			t.Fatalf("admission rejection should survive as the root cause, got %+v", out)
		}
	})

	t.Run("rollout_stalled folds into rbac_forbidden on same owner", func(t *testing.T) {
		rollout := Issue{Source: SourceProblem, Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web",
			Category: issuesapi.CategoryRolloutStalled, Severity: SeverityCritical, Reason: "ReplicaFailure"}
		rbac := Issue{Source: SourceScheduling, Group: "apps", Kind: "ReplicaSet", Namespace: "ns", Name: "web-abc",
			Owner: dep, Category: issuesapi.CategoryRBACForbidden, Severity: SeverityCritical}
		out := dedupeWorkloadDegradedOverChild([]Issue{rollout, rbac})
		if hasCategory(out, issuesapi.CategoryRolloutStalled) {
			t.Fatalf("rollout_stalled should fold into rbac_forbidden, got %+v", out)
		}
	})

	t.Run("cronjob_failed is not a rollup and survives alongside an unrelated job_failed", func(t *testing.T) {
		cron := Issue{Source: SourceProblem, Group: "batch", Kind: "CronJob", Namespace: "ns", Name: "nightly",
			Category: issuesapi.CategoryCronJobFailed, Severity: SeverityWarning, Reason: "stale"}
		// Unrelated job (different subject) with a crashloop child — must not affect the cronjob row.
		otherJob := Ref{Group: "batch", Kind: "Job", Namespace: "ns", Name: "other"}
		jobFailed := Issue{Source: SourceProblem, Group: "batch", Kind: "Job", Namespace: "ns", Name: "other",
			Category: issuesapi.CategoryJobFailed, Severity: SeverityCritical}
		child := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "other-xyz",
			Owner: otherJob, Category: issuesapi.CategoryCrashLoop, Severity: SeverityCritical}
		out := dedupeWorkloadDegradedOverChild([]Issue{cron, jobFailed, child})
		if !hasCategory(out, issuesapi.CategoryCronJobFailed) {
			t.Fatalf("cronjob_failed must never be folded as a rollup, got %+v", out)
		}
	})

	t.Run("severity gate: critical rollup with only a warning child is kept", func(t *testing.T) {
		degraded := Issue{Source: SourceProblem, Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web",
			Category: issuesapi.CategoryWorkloadDegraded, Severity: SeverityCritical, Reason: "0/3 available"}
		waiting := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "web-abc",
			Owner: dep, Category: issuesapi.CategoryContainerWaiting, Severity: SeverityWarning}
		out := dedupeWorkloadDegradedOverChild([]Issue{degraded, waiting})
		if !hasCategory(out, issuesapi.CategoryWorkloadDegraded) {
			t.Fatalf("critical rollup must not be downgraded to a warning child, got %+v", out)
		}
	})
}

func TestDedupeConditionOverMissingRef(t *testing.T) {
	missing := Issue{
		Source:    SourceMissingRef,
		Group:     "gateway.networking.k8s.io",
		Kind:      "HTTPRoute",
		Namespace: "prod",
		Name:      "broken",
		Category:  issuesapi.CategoryGatewayRouteInvalid,
	}
	conditionEcho := Issue{
		Source:    SourceCondition,
		Group:     "gateway.networking.k8s.io",
		Kind:      "HTTPRoute",
		Namespace: "prod",
		Name:      "broken",
		Reason:    "ResolvedRefs: BackendNotFound",
		Category:  issuesapi.CategoryGatewayRouteInvalid,
	}
	conditionAccepted := conditionEcho
	conditionAccepted.Reason = "Accepted: NoMatchingParent"
	conditionOtherCategory := conditionEcho
	conditionOtherCategory.Category = issuesapi.CategoryGatewayNotReady
	conditionOtherObject := conditionEcho
	conditionOtherObject.Name = "other"

	out := dedupeConditionOverMissingRef([]Issue{missing, conditionEcho, conditionAccepted, conditionOtherCategory, conditionOtherObject})
	if len(out) != 4 {
		t.Fatalf("expected only the ResolvedRefs echo to be dropped, got %+v", out)
	}
	var keptAccepted bool
	for _, i := range out {
		if i.Source == SourceCondition && i.Name == "broken" && i.Category == issuesapi.CategoryGatewayRouteInvalid && i.Reason == "ResolvedRefs: BackendNotFound" {
			t.Fatalf("same-object ResolvedRefs echo survived: %+v", out)
		}
		if i.Source == SourceCondition && i.Name == "broken" && i.Reason == "Accepted: NoMatchingParent" {
			keptAccepted = true
		}
	}
	if !keptAccepted {
		t.Fatalf("non-ResolvedRefs route condition was incorrectly dropped: %+v", out)
	}
}
