package k8s

import (
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/k8score"
)

const cronJobScheduleObservationThreshold = 3

type cronJobScheduleObservationState struct {
	schedule       string
	policy         batchv1.ConcurrencyPolicy
	suspended      bool
	lastSchedule   time.Time
	lastSuccess    time.Time
	withoutSuccess int
	sequenceSince  time.Time
}

// cronJobScheduleObservationTracker deliberately starts from observation
// rather than reconstructing missed runs from resource age. Restarts and
// policy changes reset the evidence, favoring a late warning over fabricated history.
type cronJobScheduleObservationTracker struct {
	mu      sync.Mutex
	entries map[types.UID]cronJobScheduleObservationState
}

func newCronJobScheduleObservationTracker() *cronJobScheduleObservationTracker {
	return &cronJobScheduleObservationTracker{entries: map[types.UID]cronJobScheduleObservationState{}}
}

func (t *cronJobScheduleObservationTracker) observe(operation string, cj *batchv1.CronJob) {
	if t == nil || cj == nil || cj.UID == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if operation == k8score.OpDelete {
		delete(t.entries, cj.UID)
		return
	}

	current := cronJobScheduleObservationState{
		schedule:     cj.Spec.Schedule,
		policy:       cj.Spec.ConcurrencyPolicy,
		suspended:    cj.Spec.Suspend != nil && *cj.Spec.Suspend,
		lastSchedule: cronJobStatusTime(cj.Status.LastScheduleTime),
		lastSuccess:  cronJobStatusTime(cj.Status.LastSuccessfulTime),
	}
	previous, ok := t.entries[cj.UID]
	if !ok || current.schedule != previous.schedule || current.policy != previous.policy ||
		current.suspended != previous.suspended || current.policy != batchv1.ReplaceConcurrent ||
		current.suspended || current.lastSchedule.IsZero() ||
		!current.lastSuccess.Equal(previous.lastSuccess) || current.lastSchedule.Before(previous.lastSchedule) {
		t.entries[cj.UID] = current
		return
	}

	current.withoutSuccess = previous.withoutSuccess
	current.sequenceSince = previous.sequenceSince
	if current.lastSchedule.After(previous.lastSchedule) {
		current.withoutSuccess++
		if current.sequenceSince.IsZero() {
			current.sequenceSince = current.lastSchedule
		}
	}
	t.entries[cj.UID] = current
}

func (t *cronJobScheduleObservationTracker) withoutRecordedSuccess(uid types.UID) (int, time.Time) {
	if t == nil || uid == "" {
		return 0, time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.entries[uid]
	return state.withoutSuccess, state.sequenceSince
}

func cronJobStatusTime(value *metav1.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.Time
}
