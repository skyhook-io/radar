package issuecorrelation

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestAdmissionBlocker_LimitRangeIgnoresCustomGroup(t *testing.T) {
	initCache(t)
	store := initStore(t)
	now := time.Now().Truncate(time.Second)
	for _, version := range []string{"v1", "example.com/v1"} {
		appendChange(t, store, timeline.TimelineEvent{
			ID: version, Timestamp: now.Add(-time.Minute), Kind: "LimitRange", APIVersion: version,
			Namespace: "shop", Name: "container-limits", EventType: timeline.EventTypeAdd,
		})
	}
	iss := issuesapi.Issue{
		Severity: issuesapi.SeverityCritical, Category: issuesapi.CategoryQuotaExceeded,
		Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "api",
		Message: `pod creation blocked: pods "api-6d8b-xyz12" is forbidden: maximum memory usage per Container is 512Mi, but limit is 1Gi`,
	}
	ev, readErr := lookbackRun(iss, Access{}).admissionBlocker()
	if readErr != nil || ev == nil || !ev.complete || ev.saturated || len(ev.changes) != 1 || ev.changes[0].APIVersion != "v1" {
		t.Fatalf("admission evidence = %+v (read error %+v), want complete core-only LimitRange history", ev, readErr)
	}
}

// A ConfigMap edit stays a correlated ref but changes nothing admission
// checks, so it does not decide which side changed last.
func TestAdmissionChangeOrder_ConfigMapEditIsNotAWorkloadChange(t *testing.T) {
	quotaMsg := `pods "web-x" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=3, limited: pods=3`
	now := time.Now().Truncate(time.Second)
	initCache(t)
	store := initStore(t)
	quotaAt := now.Add(-10 * time.Minute)
	appendChange(t, store, timeline.TimelineEvent{Timestamp: now.Add(-20 * time.Minute), Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeAdd})
	appendChange(t, store, timeline.TimelineEvent{Timestamp: quotaAt, Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeAdd})
	cm := []issuesapi.RecentChange{{Kind: "ConfigMap", Namespace: "shop", Name: "web-config", ChangeType: "update", Timestamp: now.Add(-2 * time.Minute).Format(time.RFC3339)}}
	ev, readErr := lookbackRun(issuesapi.Issue{Category: issuesapi.CategoryQuotaExceeded, Kind: "Deployment", Namespace: "shop", Name: "web", Message: quotaMsg}, Access{}).admissionBlocker()
	if readErr != nil || ev == nil {
		t.Fatalf("admission evidence = %+v, read error %+v", ev, readErr)
	}
	subject := append(cm, issuesapi.RecentChange{Kind: "Deployment", Namespace: "shop", Name: "web", ChangeType: "add", Timestamp: now.Add(-20 * time.Minute).Format(time.RFC3339)})
	sentence, policyLast := admissionChangeOrder(&issuesapi.Issue{Kind: "Deployment", Name: "web"}, ev, subject, true, true, "in the last hour")
	if !policyLast || !strings.HasSuffix(sentence, "the ResourceQuota changed last.") {
		t.Fatalf("a ConfigMap edit decided the order: %q", sentence)
	}
}

// Changes recorded in the same second can only be called a tie when both
// sides are complete: an unseen newer change on either side would make even
// that wrong, so incomplete evidence gets no sentence at all.
func TestAdmissionChangeOrder_SameSecondNeedsCompleteEvidence(t *testing.T) {
	at := time.Now().Truncate(time.Second).Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	policy := issuesapi.RecentChange{Kind: "ResourceQuota", Namespace: "shop", Name: "compute", ChangeType: "update", Timestamp: at}
	subject := []issuesapi.RecentChange{{Kind: "Deployment", Namespace: "shop", Name: "web", ChangeType: "update", Timestamp: at}}
	iss := &issuesapi.Issue{Kind: "Deployment", Name: "web"}
	for _, tc := range []struct {
		name                            string
		policyComplete, subjectComplete bool
		wantTie                         bool
	}{
		{"both complete", true, true, true},
		{"policy incomplete", false, true, false},
		{"subject incomplete", true, false, false},
	} {
		ev := &admissionBlockerEvidence{changes: []issuesapi.RecentChange{policy}, complete: tc.policyComplete,
			unchanged: func(string) string { return "" }}
		sentence, _ := admissionChangeOrder(iss, ev, subject, tc.subjectComplete, true, "in the last hour")
		if gotTie := strings.Contains(sentence, "same second"); gotTie != tc.wantTie || (!tc.wantTie && sentence != "") {
			t.Fatalf("%s: sentence %q", tc.name, sentence)
		}
	}
}
