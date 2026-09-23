package k8s

import (
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

const cronJobScheduleFailureWindow = 30 * time.Minute

func detectCronJobScheduleEvents(cache *ResourceCache, namespace string, cronjobs []*batchv1.CronJob, now time.Time) []Detection {
	lister := cache.Events()
	if lister == nil || len(cronjobs) == 0 {
		return nil
	}
	var events []*corev1.Event
	var err error
	if namespace == "" {
		events, err = lister.List(labels.Everything())
	} else {
		events, err = lister.Events(namespace).List(labels.Everything())
	}
	if err != nil {
		return nil
	}
	return cronJobScheduleEventDetections(cronjobs, events, now)
}

func cronJobScheduleEventDetections(cronjobs []*batchv1.CronJob, events []*corev1.Event, now time.Time) []Detection {
	subjects := make(map[types.NamespacedName]*batchv1.CronJob, len(cronjobs))
	for _, cj := range cronjobs {
		subjects[types.NamespacedName{Namespace: cj.Namespace, Name: cj.Name}] = cj
	}
	type eventKey struct {
		subject types.NamespacedName
		reason  string
	}
	latest := map[eventKey]*corev1.Event{}
	for _, event := range events {
		obj := event.InvolvedObject
		if event.Type != corev1.EventTypeWarning || obj.Kind != "CronJob" ||
			(obj.APIVersion != "" && GroupFromAPIVersion(obj.APIVersion) != "batch") ||
			!IsCronJobScheduleFailureReason(event.Reason) || !cronJobControllerEvent(event) {
			continue
		}
		key := eventKey{subject: types.NamespacedName{Namespace: obj.Namespace, Name: obj.Name}, reason: event.Reason}
		cj := subjects[key.subject]
		if cj == nil || (obj.UID != "" && obj.UID != cj.UID) {
			continue
		}
		// Without a UID, an aggregate that began before this CronJob existed
		// cannot safely be attributed to the current incarnation.
		if obj.UID == "" {
			first := eventFirstTime(event)
			if first.IsZero() {
				first = event.CreationTimestamp.Time
			}
			if !first.IsZero() && first.Before(cj.CreationTimestamp.Time) {
				continue
			}
		}
		last := cronJobEventLastTime(event)
		if last.IsZero() || last.After(now) || now.Sub(last) > cronJobScheduleFailureWindow || last.Before(cj.CreationTimestamp.Time) {
			continue
		}
		current := latest[key]
		if current == nil || last.After(cronJobEventLastTime(current)) ||
			(last.Equal(cronJobEventLastTime(current)) && (cronJobEventCount(event) > cronJobEventCount(current) ||
				(cronJobEventCount(event) == cronJobEventCount(current) && event.Message < current.Message))) {
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
		age := now.Sub(cj.CreationTimestamp.Time)
		detection := Detection{
			Kind: "CronJob", Group: "batch", Namespace: cj.Namespace, Name: cj.Name,
			Severity: "medium", Reason: event.Reason, Message: event.Message,
			Fingerprint: "cronjob-event:" + event.Reason,
			Age:         FormatAge(age), AgeSeconds: int64(age.Seconds()), ResourceCreatedAt: cj.CreationTimestamp.Time,
		}
		setDetectionOnset(&detection, now, eventFirstTime(event))
		detections = append(detections, detection)
	}
	return detections
}

func cronJobEventLastTime(event *corev1.Event) time.Time {
	// EventTime is the first occurrence for series-style Events.
	if event.Series != nil && !event.Series.LastObservedTime.IsZero() {
		return event.Series.LastObservedTime.Time
	}
	return eventLastTime(event)
}

func cronJobEventCount(event *corev1.Event) int32 {
	if event.Series != nil && event.Series.Count > 0 {
		return event.Series.Count
	}
	return max(event.Count, 1)
}

func cronJobControllerEvent(event *corev1.Event) bool {
	const controller = "cronjob-controller"
	if event.Source.Component == "" && event.ReportingController == "" {
		return false
	}
	if event.Source.Component != "" && event.Source.Component != controller {
		return false
	}
	return event.ReportingController == "" || event.ReportingController == controller ||
		event.ReportingController == "kubernetes.io/"+controller
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
