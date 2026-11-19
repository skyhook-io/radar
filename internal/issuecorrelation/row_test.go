package issuecorrelation

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestRowChange(t *testing.T) {
	firstSeen := time.Now().Truncate(time.Second).Add(-time.Hour)
	at := func(d time.Duration, kind string) issuesapi.RecentChange {
		return issuesapi.RecentChange{Kind: kind, Name: "web", EventID: kind + d.String(), Timestamp: firstSeen.Add(d).UTC().Format(time.RFC3339),
			Fields: []issuesapi.ChangeField{{Path: "spec.template.spec.containers[web].image", OldValue: "web:1", NewValue: "web:2"}}}
	}
	m := time.Minute
	for _, tc := range []struct {
		name    string
		iss     issuesapi.Issue
		want    string
		wantNot bool
	}{
		{"newest at or before first seen", issuesapi.Issue{FirstSeen: firstSeen, CorrelatedChanges: []issuesapi.RecentChange{
			at(-20*m, "ConfigMap"), at(-4*m, "Deployment"), at(5*m, "Service"), at(30*m, "Secret")}}, "Deployment-4m0s", false},
		{"within the slop counts as before", issuesapi.Issue{FirstSeen: firstSeen, CorrelatedChanges: []issuesapi.RecentChange{
			at(-20*m, "ConfigMap"), at(20*time.Second, "Deployment")}}, "Deployment20s", false},
		{"never a change after first seen", issuesapi.Issue{FirstSeen: firstSeen, CorrelatedChanges: []issuesapi.RecentChange{
			at(2*m, "Deployment"), at(40*m, "ConfigMap")}}, "", true},
		{"no first seen: the newest in the window", issuesapi.Issue{OnsetUnknown: true, CorrelatedChanges: []issuesapi.RecentChange{
			at(-20*m, "ConfigMap"), at(40*m, "Deployment"), at(5*m, "Service")}}, "Deployment40m0s", false},
		{"no changes", issuesapi.Issue{FirstSeen: firstSeen}, "", true},
		{"a source Radar couldn't read may hold a newer change", issuesapi.Issue{FirstSeen: firstSeen,
			Correlation:       &issuesapi.ChangeCorrelation{Unknown: &issuesapi.CorrelationUnknown{Reason: issuesapi.CorrelationUnknownReadError}},
			CorrelatedChanges: []issuesapi.RecentChange{at(-4*m, "Deployment")}}, "", true},
		{"a source the caller can't read may hold a newer change", issuesapi.Issue{FirstSeen: firstSeen,
			Correlation:       &issuesapi.ChangeCorrelation{Unknown: &issuesapi.CorrelationUnknown{Reason: issuesapi.CorrelationUnknownUserForbidden}},
			CorrelatedChanges: []issuesapi.RecentChange{at(-4*m, "Deployment")}}, "", true},
		{"history missing from before the window can't hold a newer change", issuesapi.Issue{FirstSeen: firstSeen,
			Correlation:       &issuesapi.ChangeCorrelation{Unknown: &issuesapi.CorrelationUnknown{Reason: issuesapi.CorrelationUnknownHistoryIncomplete}},
			CorrelatedChanges: []issuesapi.RecentChange{at(-4*m, "Deployment")}}, "Deployment-4m0s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RowChange(tc.iss)
			if ok == tc.wantNot || got.EventID != tc.want {
				t.Fatalf("got %q (ok %v), want %q", got.EventID, ok, tc.want)
			}
			if ok && (got.Fields[0].OldValue != nil || got.Fields[0].NewValue != nil || got.Fields[0].Path == "") {
				t.Fatalf("row kept field values: %+v", got.Fields)
			}
		})
	}
}
