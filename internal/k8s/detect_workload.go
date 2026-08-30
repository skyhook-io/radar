package k8s

import (
	"fmt"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/cronsched"
	"github.com/skyhook-io/radar/pkg/hpadiag"
)

// HPAProblem describes a detected issue with an HPA.
type HPAProblem struct {
	Name      string
	Namespace string
	Problem   string // "maxed" or "cannot-scale"
	Reason    string
	Cause     string
	Action    string
}

// DetectHPAProblems finds HPAs that have hit their replica ceiling OR that
// cannot scale because the autoscaler can't fetch metrics. The latter is
// the silent-broken-HPA case: spec is valid, target exists, but
// status.conditions[?type=ScalingActive].status=False means the controller
// gave up — metrics.k8s.io unavailable, broken adapter, missing resource
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
			cause, action := hpaCannotScaleDiagnosis(reason, hpa.Namespace)
			problems = append(problems, HPAProblem{
				Name:      hpa.Name,
				Namespace: hpa.Namespace,
				Problem:   "cannot-scale",
				Reason:    reasonText(reason),
				Cause:     cause,
				Action:    action,
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

func hpaCannotScaleDiagnosis(reason hpadiag.Reason, namespace string) (string, string) {
	text := strings.ToLower(reason.ConditionReason + " " + reason.Message)
	switch {
	case hasHPAConditionReason(reason, "FailedGetResourceMetric") || hasHPAConditionReason(reason, "FailedGetContainerResourceMetric"):
		if metric, ok := missingHPARequestMetric(text); ok {
			containerResource := hasHPAConditionReason(reason, "FailedGetContainerResourceMetric")
			target := "the HPA target workload's containers"
			if containerResource {
				target = "the container measured by the HPA's ContainerResource metric"
			}
			if metric == "" {
				return "Target pods do not declare the required resource requests, so the HPA cannot compute utilization.",
					"Add the missing resource requests to " + target + ", then wait for the HPA to refresh."
			}
			return fmt.Sprintf("Target pods do not declare %s resource requests, so the HPA cannot compute utilization.", metric),
				fmt.Sprintf("Add %s resource requests to %s, then wait for the HPA to refresh.", metric, target)
		}
		namespacePhrase := "the HPA namespace"
		if namespace != "" {
			namespacePhrase = "namespace " + namespace
		}
		return "The resource metrics API (metrics.k8s.io) is unavailable or not returning pod metrics.",
			"Verify a resource-metrics provider, typically metrics-server, is installed and that pod metrics are available in " + namespacePhrase + "."
	case hasHPAConditionReason(reason, "FailedGetPodsMetric") || hasHPAConditionReason(reason, "FailedGetObjectMetric"):
		return "The custom metrics adapter could not return the HPA's custom metric.",
			"Check the custom metrics adapter and verify the metric name, selector, and namespace match what the HPA requests."
	case hasHPAConditionReason(reason, "FailedGetExternalMetric"):
		return "The external metrics adapter could not return the HPA's external metric.",
			"Check the external metrics adapter and verify the metric name, selector, and backing query are available to the HPA."
	case hasHPAConditionReason(reason, "FailedGetScale") || hasHPAConditionReason(reason, "FailedUpdateScale") || hasHPAConditionReason(reason, "FailedRescale") || reason.ID == hpadiag.ReasonUnableToScale:
		return "The HPA controller cannot read or update the target's scale subresource.",
			"Verify the scaleTargetRef exists, exposes the scale subresource, and allows the HPA controller to update it."
	default:
		return "The HPA controller cannot read one or more scaling metrics.",
			"Inspect the HPA conditions and the relevant metrics adapter logs to identify which metric is unavailable."
	}
}

func hasHPAConditionReason(reason hpadiag.Reason, token string) bool {
	// The HPA controller emits stable FailedGet* reason tokens, and hpadiag
	// copies cond.Reason verbatim. Adapter messages vary, so keep classification
	// anchored to the structured reason instead of substring-matching prose.
	return strings.EqualFold(reason.ConditionReason, token)
}

func missingHPARequestMetric(text string) (string, bool) {
	const marker = "missing request for "
	idx := strings.Index(text, marker)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(text[idx+len(marker):])
	if rest == "" {
		return "", true
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", true
	}
	metric := strings.Trim(fields[0], `.,;:()[]{}"'`)
	switch strings.ToLower(metric) {
	case "cpu":
		return "CPU", true
	case "memory":
		return "memory", true
	default:
		return "", true
	}
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
	Problem   string // "stale", "never-scheduled", or "repeated-without-success"
	Reason    string
	Duration  time.Duration
	OnsetAt   time.Time
}

// DetectCronJobProblems finds non-suspended CronJobs whose schedule or success
// history indicates that they are not producing successful runs.
func DetectCronJobProblems(cronjobs []*batchv1.CronJob, jobs []*batchv1.Job, tracker *cronJobScheduleObservationTracker, now time.Time) []CronJobProblem {
	var problems []CronJobProblem
	jobProblemOwners := cronJobOwnersWithJobProblems(jobs, now)
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
					OnsetAt:   cj.Status.LastScheduleTime.Add(threshold),
				})
				continue
			}
			if !jobProblemOwners[cj.UID] {
				if reason, onsetAt, ok := repeatedCronJobSchedulesWithoutSuccess(cj, tracker, now); ok {
					problems = append(problems, CronJobProblem{
						Name:      cj.Name,
						Namespace: cj.Namespace,
						Problem:   "repeated-without-success",
						Reason:    reason,
						Duration:  now.Sub(onsetAt),
						OnsetAt:   onsetAt,
					})
				}
			}
		} else if now.Sub(cj.CreationTimestamp.Time) > threshold {
			problems = append(problems, CronJobProblem{
				Name:      cj.Name,
				Namespace: cj.Namespace,
				Problem:   "never-scheduled",
				Reason:    "created but never ran",
				OnsetAt:   cj.CreationTimestamp.Add(threshold),
			})
		}
	}
	return problems
}

func cronJobOwnersWithJobProblems(jobs []*batchv1.Job, now time.Time) map[types.UID]bool {
	owners := map[types.UID]bool{}
	for _, job := range jobs {
		if !jobHasDetectedProblem(job, now) {
			continue
		}
		controller := metav1.GetControllerOf(job)
		if controller != nil && controller.Kind == "CronJob" && controller.UID != "" {
			owners[controller.UID] = true
		}
	}
	return owners
}

func jobHasDetectedProblem(job *batchv1.Job, now time.Time) bool {
	if job == nil {
		return false
	}
	if _, ok := terminatingProblem("Job", "batch", job, now); ok {
		return true
	}
	return failedJobCondition(job) != nil || stuckActiveJob(job, now)
}

func stuckActiveJob(job *batchv1.Job, now time.Time) bool {
	return job != nil && job.Status.Active > 0 && job.Status.Succeeded == 0 && job.Status.Failed == 0 &&
		now.Sub(job.CreationTimestamp.Time) > time.Hour
}

func repeatedCronJobSchedulesWithoutSuccess(cj *batchv1.CronJob, tracker *cronJobScheduleObservationTracker, now time.Time) (string, time.Time, bool) {
	// Replace can delete an unfinished Job before the Job detectors accumulate
	// evidence. Allow retains Jobs for those detectors, while Forbid stops
	// advancing lastScheduleTime and is covered as stale.
	if cj.Spec.ConcurrencyPolicy != batchv1.ReplaceConcurrent || len(cj.Status.Active) == 0 {
		return "", time.Time{}, false
	}

	if cj.Status.LastScheduleTime == nil || cj.Status.LastScheduleTime.Time.After(now) {
		return "", time.Time{}, false
	}
	schedules, sequenceSince := tracker.withoutRecordedSuccess(cj.UID)
	if schedules < cronJobScheduleObservationThreshold || sequenceSince.IsZero() || sequenceSince.After(now) {
		return "", time.Time{}, false
	}
	if success := cj.Status.LastSuccessfulTime; success != nil && !success.IsZero() && !success.Time.Before(sequenceSince) {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("%d consecutive schedules observed without a recorded success; last scheduled %s ago",
		schedules, FormatAge(now.Sub(cj.Status.LastScheduleTime.Time))), sequenceSince, true
}
