package k8s

import (
	"fmt"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"

	"github.com/skyhook-io/radar/pkg/cronsched"
	"github.com/skyhook-io/radar/pkg/hpadiag"
)

// HPAProblem describes a detected issue with an HPA.
type HPAProblem struct {
	Name      string
	Namespace string
	Problem   string // "maxed" or "cannot-scale"
	Reason    string
}

// DetectHPAProblems finds HPAs that have hit their replica ceiling OR that
// cannot scale because the autoscaler can't fetch metrics. The latter is
// the silent-broken-HPA case: spec is valid, target exists, but
// status.conditions[?type=ScalingActive].status=False means the controller
// gave up — metrics-server unavailable, broken adapter, missing resource
// requests on target pods, etc. K8s autoscaler condition reasons are
// stable across versions (FailedGetResourceMetric / FailedGetScale /
// FailedGetExternalMetric / FailedGetObjectMetric).
func DetectHPAProblems(hpas []*autoscalingv2.HorizontalPodAutoscaler) []HPAProblem {
	var problems []HPAProblem
	for _, hpa := range hpas {
		diagnosis := hpadiag.Analyze(hpa)
		if diagnosis == nil {
			continue
		}

		if reason, ok := firstHPAReason(diagnosis, hpadiag.ReasonLimitedMax); ok {
			problems = append(problems, HPAProblem{
				Name:      hpa.Name,
				Namespace: hpa.Namespace,
				Problem:   "maxed",
				Reason:    maxedReasonText(diagnosis, reason),
			})
		}

		if reason, ok := firstHPAReason(diagnosis, hpadiag.ReasonUnableToScale, hpadiag.ReasonMetricsUnavailable); ok {
			problems = append(problems, HPAProblem{
				Name:      hpa.Name,
				Namespace: hpa.Namespace,
				Problem:   "cannot-scale",
				Reason:    reasonText(reason),
			})
		}
	}
	return problems
}

func firstHPAReason(diagnosis *hpadiag.Diagnosis, ids ...hpadiag.ReasonID) (hpadiag.Reason, bool) {
	for _, id := range ids {
		for _, reason := range diagnosis.Reasons {
			if reason.ID == id {
				return reason, true
			}
		}
	}
	return hpadiag.Reason{}, false
}

func reasonText(reason hpadiag.Reason) string {
	if reason.ConditionReason != "" && reason.Message != "" {
		return reason.ConditionReason + ": " + reason.Message
	}
	if reason.Message != "" {
		return reason.Message
	}
	return string(reason.ID)
}

func maxedReasonText(diagnosis *hpadiag.Diagnosis, reason hpadiag.Reason) string {
	if diagnosis == nil || diagnosis.Bounds.Max <= 0 {
		return reasonText(reason)
	}
	text := fmt.Sprintf("%d/%d replicas", diagnosis.Bounds.Current, diagnosis.Bounds.Max)
	if diagnosis.Bounds.Desired > 0 {
		text += fmt.Sprintf(" (wants %d)", diagnosis.Bounds.Desired)
	}
	if detail := reasonText(reason); detail != "" {
		return text + ": " + detail
	}
	return text
}

// CronJobProblem describes a detected issue with a CronJob.
type CronJobProblem struct {
	Name      string
	Namespace string
	Problem   string // "stale" or "never-scheduled"
	Reason    string
}

// DetectCronJobProblems finds non-suspended CronJobs that haven't run recently.
func DetectCronJobProblems(cronjobs []*batchv1.CronJob) []CronJobProblem {
	var problems []CronJobProblem
	now := time.Now()
	for _, cj := range cronjobs {
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			continue
		}
		threshold := cronsched.StaleThreshold(cj.Spec.Schedule)
		if cj.Status.LastScheduleTime != nil {
			sinceLast := now.Sub(cj.Status.LastScheduleTime.Time)
			if sinceLast > threshold {
				problems = append(problems, CronJobProblem{
					Name:      cj.Name,
					Namespace: cj.Namespace,
					Problem:   "stale",
					Reason:    fmt.Sprintf("last run %dh ago", int(sinceLast.Hours())),
				})
			}
		} else if now.Sub(cj.CreationTimestamp.Time) > threshold {
			problems = append(problems, CronJobProblem{
				Name:      cj.Name,
				Namespace: cj.Namespace,
				Problem:   "never-scheduled",
				Reason:    "created but never ran",
			})
		}
	}
	return problems
}
