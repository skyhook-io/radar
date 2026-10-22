package k8s

import (
	"sort"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/timeutil"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

func detectCronJobScheduleEvents(cache *ResourceCache, namespace string, cronjobs []*batchv1.CronJob, now time.Time) cronJobSchedulingAssessment {
	lister := cache.Events()
	if lister == nil || len(cronjobs) == 0 {
		return cronJobSchedulingAssessment{}
	}
	var events []*corev1.Event
	var err error
	if namespace == "" {
		events, err = lister.List(labels.Everything())
	} else {
		events, err = lister.Events(namespace).List(labels.Everything())
	}
	if err != nil {
		return cronJobSchedulingAssessment{}
	}
	history := readCronJobSchedulingHistory(namespace, cronjobs, events, now)
	return assessCronJobScheduling(cronjobs, events, history, now)
}

func cronJobEventMatches(event *corev1.Event, cj *batchv1.CronJob, now time.Time) bool {
	obj := event.InvolvedObject
	created := event.Type == corev1.EventTypeNormal && event.Reason == "SuccessfulCreate"
	if (!created && (event.Type != corev1.EventTypeWarning || !IsCronJobScheduleFailureReason(event.Reason))) ||
		obj.Kind != "CronJob" || obj.Namespace != cj.Namespace || obj.Name != cj.Name ||
		(obj.APIVersion != "" && resourceid.GroupFromAPIVersion(obj.APIVersion) != "batch") ||
		(obj.UID == "" || obj.UID != cj.UID) {
		return false
	}
	last := eventLastTime(event)
	return !last.IsZero() && !last.After(now) && !last.Before(cj.CreationTimestamp.Time)
}

func newerCronJobEvent(event, current *corev1.Event) bool {
	last, previous := eventLastTime(event), eventLastTime(current)
	return last.After(previous) || (last.Equal(previous) && (cronJobEventCount(event) > cronJobEventCount(current) ||
		(cronJobEventCount(event) == cronJobEventCount(current) && event.Message < current.Message)))
}

func cronJobScheduleEventDetections(cronjobs []*batchv1.CronJob, events []*corev1.Event, now time.Time) []Detection {
	subjects := make(map[types.NamespacedName]*batchv1.CronJob, len(cronjobs))
	for _, cj := range cronjobs {
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			continue
		}
		subjects[types.NamespacedName{Namespace: cj.Namespace, Name: cj.Name}] = cj
	}
	type eventKey struct {
		subject types.NamespacedName
		reason  string
	}
	latest := map[eventKey]*corev1.Event{}
	lastCreated := map[types.NamespacedName]time.Time{}
	for _, event := range events {
		obj := event.InvolvedObject
		created := event.Type == corev1.EventTypeNormal && event.Reason == "SuccessfulCreate"
		key := eventKey{subject: types.NamespacedName{Namespace: obj.Namespace, Name: obj.Name}, reason: event.Reason}
		cj := subjects[key.subject]
		if cj == nil || !cronJobEventMatches(event, cj, now) {
			continue
		}
		last := eventLastTime(event)
		if created {
			if last.After(lastCreated[key.subject]) {
				lastCreated[key.subject] = last
			}
			continue
		}
		// A later scheduled run supersedes scheduling-failure evidence. A
		// previous run completing successfully does not prove scheduling recovered.
		if cj.Status.LastScheduleTime != nil && cj.Status.LastScheduleTime.After(last) {
			continue
		}
		current := latest[key]
		if current == nil || newerCronJobEvent(event, current) {
			latest[key] = event
		}
	}
	keys := make([]eventKey, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.subject.Namespace != b.subject.Namespace {
			return a.subject.Namespace < b.subject.Namespace
		}
		if a.subject.Name != b.subject.Name {
			return a.subject.Name < b.subject.Name
		}
		return a.reason < b.reason
	})
	var detections []Detection
	for _, key := range keys {
		event, cj := latest[key], subjects[key.subject]
		last := eventLastTime(event)
		created := lastCreated[key.subject]
		// Catch-up runs have a nominal schedule time before the warning.
		// Legacy Events only have second precision, so the advisory warning
		// and successful creation from the same reconciliation can tie.
		if created.After(last) || (key.reason == "TooManyMissedTimes" && created.Equal(last)) {
			continue
		}
		age := now.Sub(cj.CreationTimestamp.Time)
		detection := Detection{
			Kind: "CronJob", Group: "batch", Namespace: cj.Namespace, Name: cj.Name,
			Severity: "medium", Reason: event.Reason, Message: event.Message,
			Fingerprint: "cronjob-event:" + event.Reason,
			Age:         timeutil.FormatAgeShort(age), AgeSeconds: int64(age.Seconds()), ResourceCreatedAt: cj.CreationTimestamp.Time,
		}
		setDetectionOnset(&detection, now, eventFirstTime(event))
		detections = append(detections, detection)
	}
	return detections
}

func cronJobEventCount(event *corev1.Event) int32 {
	return k8score.EventOccurrences(event)
}

// IsCronJobScheduleFailureReason identifies controller scheduling failures, not
// warnings such as UnsupportedSchedule that can still allow a run.
func IsCronJobScheduleFailureReason(reason string) bool {
	switch reason {
	case "UnknownTimeZone", "UnParseableCronJobSchedule", "UnparseableSchedule", "InvalidSchedule", "MissSchedule", "TooManyMissedTimes":
		return true
	default:
		return false
	}
}
