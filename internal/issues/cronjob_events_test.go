package issues

import (
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestCronJobControllerEventIssues(t *testing.T) {
	now := time.Now()
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "batch", UID: "current", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
		Spec:       batchv1.CronJobSpec{Schedule: "0 * * * *"},
	}
	reasons := []string{"UnknownTimeZone", "UnParseableCronJobSchedule", "UnparseableSchedule", "InvalidSchedule", "MissSchedule", "TooManyMissedTimes"}
	objects := []runtime.Object{cj}
	messages := map[string]string{}
	for _, reason := range reasons {
		messages[reason] = "Controller evidence: " + reason
		objects = append(objects, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: strings.ToLower(reason), Namespace: cj.Namespace},
			InvolvedObject: corev1.ObjectReference{APIVersion: "batch/v1", Kind: "CronJob", Namespace: cj.Namespace, Name: cj.Name, UID: cj.UID},
			Type:           corev1.EventTypeWarning, Reason: reason, Message: messages[reason],
			EventTime: metav1.NewMicroTime(now.Add(-2 * time.Minute)), Source: corev1.EventSource{Component: "cronjob-controller"},
		})
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: fake.NewClientset(objects...), ResourceTypes: map[string]bool{k8score.CronJobs: true, k8score.Events: true}, DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	provider := &fakeProvider{problems: k8s.DetectProblems(cache, cj.Namespace)}
	for _, grouped := range []bool{false, true} {
		got := Compose(provider, Filters{Grouped: grouped})
		if len(got) != len(reasons) {
			t.Fatalf("grouped=%v: got %d issues, want one per reason: %+v", grouped, len(got), got)
		}
		seen := map[string]bool{}
		for _, issue := range got {
			if seen[issue.Reason] || messages[issue.Reason] == "" {
				t.Errorf("unexpected/duplicate reason: %+v", issue)
			}
			seen[issue.Reason] = true
			if issue.Category != issuesapi.CategoryCronJobFailed || issue.Kind != "CronJob" || issue.Group != "batch" || issue.Name != cj.Name || issue.Namespace != cj.Namespace {
				t.Errorf("wrong classification/subject: %+v", issue)
			}
			if issue.Message != messages[issue.Reason] || issue.Cause != "" || issue.Action != "" || issue.RemediationKind != "" || issue.RemediationTarget != "" {
				t.Errorf("controller evidence was lost or inferred guidance added: %+v", issue)
			}
		}
	}
}

func TestCronJobControllerEventCategory(t *testing.T) {
	for _, reason := range []string{"UnknownTimeZone", "UnParseableCronJobSchedule", "UnparseableSchedule", "InvalidSchedule", "MissSchedule", "TooManyMissedTimes"} {
		for _, message := range []string{"controller details", `resource is forbidden: User "x" cannot use this schedule`} {
			t.Run(reason+"/"+message, func(t *testing.T) {
				got := Classify(classifyInput{Source: SourceProblem, Kind: "CronJob", APIGroup: "batch", Reason: reason, Message: message})
				if got != issuesapi.CategoryCronJobFailed {
					t.Fatalf("category = %s, want cronjob_failed", got)
				}
			})
		}
	}
	if got := Classify(classifyInput{Source: SourceProblem, Kind: "CronJob", Reason: "UnsupportedSchedule"}); got != issuesapi.CategoryUnknown {
		t.Fatalf("UnsupportedSchedule incorrectly classified: %s", got)
	}
}
