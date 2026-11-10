package k8s

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
)

type observedCall struct {
	who, kind, op string
}

type observedLog struct {
	mu    sync.Mutex
	calls []observedCall
}

func (l *observedLog) observer(who string, want kubernetes.Interface) ResourceObserver {
	return func(source kubernetes.Interface, change ResourceChange, _, _ any) {
		if source != want {
			return
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		l.calls = append(l.calls, observedCall{who, change.Kind, change.Operation})
	}
}

func (l *observedLog) has(c observedCall) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Contains(l.calls, c)
}

func (l *observedLog) snapshot() []observedCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// TestObservedChangeHandlerDeliversEveryChange pins the observer contract
// the cache's own consumers rely on: every observer, cache-bound or
// process-wide, sees initial-sync adds and updates the noisy filter keeps
// off the changes channel, cache-bound observers run first, and the secret
// index and CronJob tracker still record what they observe.
func TestObservedChangeHandlerDeliversEveryChange(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "credentials", UID: types.UID("secret-uid"), ResourceVersion: "1"}}
	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "report", UID: types.UID("cron-uid"), ResourceVersion: "1"},
		Spec:       batchv1.CronJobSpec{Schedule: "*/5 * * * *"},
	}
	client := fake.NewClientset(secret, cronJob)

	var log observedLog
	OnResourceObserved(log.observer("global", client))
	secretWriteTimes := newSecretDataManagerWriteIndex()
	cronTracker := newCronJobScheduleObservationTracker()

	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        client,
		ResourceTypes: map[string]bool{k8score.Secrets: true, k8score.CronJobs: true},
		DeferredTypes: map[string]bool{},
		OnTransform:   secretWriteTimes.capture,
		OnObservedChange: observedChangeHandler(client,
			secretWriteTimes.observeChange,
			cronTracker.observeChange,
			log.observer("cache", client),
		),
		// Every update is noisy and initial adds are suppressed from OnChange:
		// the observers must still see both.
		IsNoisyResource:     func(_, _, op string) bool { return op == "update" },
		SuppressInitialAdds: true,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)

	updated := secret.DeepCopy()
	updated.ResourceVersion = "2"
	updated.Annotations = map[string]string{"rotation": "checked"}
	if _, err := client.CoreV1().Secrets("shop").Update(context.Background(), updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	want := []observedCall{
		{"cache", "Secret", "add"}, {"global", "Secret", "add"},
		{"cache", "CronJob", "add"}, {"global", "CronJob", "add"},
		{"cache", "Secret", "update"}, {"global", "Secret", "update"},
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !slices.ContainsFunc(want, func(c observedCall) bool { return !log.has(c) }) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := log.snapshot()
	for _, c := range want {
		if !slices.Contains(calls, c) {
			t.Fatalf("missing %+v; observed %+v", c, calls)
		}
	}
	// Each change reaches the cache-bound observer before the process-wide
	// one. Informers deliver concurrently, so compare counts per change kind
	// rather than adjacency.
	seen := map[observedCall]int{}
	for _, c := range calls {
		seen[c]++
		if c.who == "global" && seen[observedCall{"cache", c.kind, c.op}] < seen[c] {
			t.Fatalf("process-wide observer ran before the cache's own for %+v: %+v", c, calls)
		}
	}

	cronTracker.mu.Lock()
	_, tracked := cronTracker.entries[cronJob.UID]
	cronTracker.mu.Unlock()
	if !tracked {
		t.Fatal("CronJob schedule tracker did not record the observed CronJob")
	}
	secretWriteTimes.mu.Lock()
	pending := len(secretWriteTimes.pending)
	secretWriteTimes.mu.Unlock()
	if pending != 0 {
		t.Fatalf("secret write index left %d captured versions unreconciled", pending)
	}
}
