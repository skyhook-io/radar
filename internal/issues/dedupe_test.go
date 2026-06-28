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

func TestStructuralRootOverSymptom_Phase1(t *testing.T) {
	dep := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}

	has := func(out []Issue, src Source, c issuesapi.Category, name string) bool {
		for _, i := range out {
			if i.Source == src && i.Category == c && i.Name == name {
				return true
			}
		}
		return false
	}
	sevOf := func(out []Issue, c issuesapi.Category, name string) Severity {
		for _, i := range out {
			if i.Category == c && i.Name == name {
				return i.Severity
			}
		}
		return ""
	}

	t.Run("missing Secret folds container_waiting on same pod", func(t *testing.T) {
		missing := Issue{Source: SourceMissingRef, Kind: "Pod", Namespace: "ns", Name: "web-abc", Owner: dep,
			Category: issuesapi.CategoryMissingConfigRef, Reason: "Missing Secret", Severity: SeverityCritical}
		waiting := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "web-abc", Owner: dep,
			Category: issuesapi.CategoryContainerWaiting, Reason: "CreateContainerConfigError", Severity: SeverityWarning}
		out := dedupeContainerWaitingOverMissingRef([]Issue{missing, waiting})
		if has(out, SourceProblem, issuesapi.CategoryContainerWaiting, "web-abc") {
			t.Fatalf("container_waiting should fold into the missing-Secret root, got %+v", out)
		}
		if !has(out, SourceMissingRef, issuesapi.CategoryMissingConfigRef, "web-abc") {
			t.Fatalf("missing-ref root should survive, got %+v", out)
		}
	})

	t.Run("image_pull_failed is NOT folded under a missing imagePullSecret", func(t *testing.T) {
		// We deliberately don't fold image_pull_failed: an ImagePullBackOff
		// alongside a missing pull secret is usually an unrelated pull error
		// (wrong tag / not-found / rate-limit) that must survive.
		missing := Issue{Source: SourceMissingRef, Kind: "Pod", Namespace: "ns", Name: "web-abc",
			Category: issuesapi.CategoryMissingConfigRef, Reason: "Missing imagePullSecret", Severity: SeverityCritical}
		imgPull := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "web-abc",
			Category: issuesapi.CategoryImagePullFailed, Reason: "ImagePullBackOff", Severity: SeverityWarning}
		out := dedupeContainerWaitingOverMissingRef([]Issue{missing, imgPull})
		if !has(out, SourceProblem, issuesapi.CategoryImagePullFailed, "web-abc") {
			t.Fatalf("image_pull_failed must survive — it is not folded by the missing-ref passes, got %+v", out)
		}
	})

	t.Run("missing imagePullSecret folds the pod's CreateContainerConfigError", func(t *testing.T) {
		// A *missing* pull-secret object manifests as CreateContainerConfigError;
		// that container_waiting row folds into the missing-ref root.
		missing := Issue{Source: SourceMissingRef, Kind: "Pod", Namespace: "ns", Name: "web-abc",
			Category: issuesapi.CategoryMissingConfigRef, Reason: "Missing imagePullSecret", Severity: SeverityCritical}
		waiting := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "web-abc",
			Category: issuesapi.CategoryContainerWaiting, Reason: "CreateContainerConfigError", Severity: SeverityWarning}
		out := dedupeContainerWaitingOverMissingRef([]Issue{missing, waiting})
		if has(out, SourceProblem, issuesapi.CategoryContainerWaiting, "web-abc") {
			t.Fatalf("container_waiting should fold into the missing-imagePullSecret root, got %+v", out)
		}
	})

	t.Run("HPA missing target folds the hpa condition row and promotes severity", func(t *testing.T) {
		missing := Issue{Source: SourceMissingRef, Kind: "HorizontalPodAutoscaler", Group: "autoscaling", Namespace: "ns", Name: "web-hpa",
			Category: issuesapi.CategoryMissingConfigRef, Reason: "Missing scaleTargetRef", Severity: SeverityWarning}
		cond := Issue{Source: SourceProblem, Kind: "HorizontalPodAutoscaler", Group: "autoscaling", Namespace: "ns", Name: "web-hpa",
			Category: issuesapi.CategoryHPALimitedOrFailed, Severity: SeverityCritical}
		out := dedupeHPAOverMissingTarget([]Issue{missing, cond})
		if has(out, SourceProblem, issuesapi.CategoryHPALimitedOrFailed, "web-hpa") {
			t.Fatalf("hpa condition should fold into the missing-target root, got %+v", out)
		}
		// Floor preserved: warning root absorbing a critical symptom is promoted.
		if sevOf(out, issuesapi.CategoryMissingConfigRef, "web-hpa") != SeverityCritical {
			t.Fatalf("surviving root must be promoted to critical so folding doesn't downgrade, got %s", sevOf(out, issuesapi.CategoryMissingConfigRef, "web-hpa"))
		}
	})

	t.Run("no missing-ref root is a no-op", func(t *testing.T) {
		waiting := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "lonely",
			Category: issuesapi.CategoryContainerWaiting, Severity: SeverityWarning}
		out := dedupeContainerWaitingOverMissingRef([]Issue{waiting})
		if len(out) != 1 {
			t.Fatalf("expected the lone container_waiting to survive, got %+v", out)
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
