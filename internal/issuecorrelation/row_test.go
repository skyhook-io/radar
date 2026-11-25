package issuecorrelation

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestRowChange(t *testing.T) {
	firstSeen := time.Now().Truncate(time.Second).Add(-time.Hour)
	at := func(d time.Duration, kind string) issuesapi.RecentChange {
		return issuesapi.RecentChange{Kind: kind, Name: "web", EventID: kind + d.String(), Timestamp: firstSeen.Add(d).UTC().Format(time.RFC3339),
			Fields: []issuesapi.ChangeField{{Path: "spec.template.spec.containers[web].image", OldValue: "web:1", NewValue: "web:2"}}}
	}
	m := time.Minute
	none := issuesapi.HistoryCoverage{}
	historyGap := issuesapi.Issue{FirstSeen: firstSeen,
		Correlation:       &issuesapi.ChangeCorrelation{Unknown: &issuesapi.CorrelationUnknown{Reason: issuesapi.CorrelationUnknownHistoryIncomplete}},
		CorrelatedChanges: []issuesapi.RecentChange{at(-4*m, "Deployment")}}
	for _, tc := range []struct {
		name    string
		iss     issuesapi.Issue
		history issuesapi.HistoryCoverage
		want    string
		wantNot bool
	}{
		{"newest at or before first seen", issuesapi.Issue{FirstSeen: firstSeen, CorrelatedChanges: []issuesapi.RecentChange{
			at(-20*m, "ConfigMap"), at(-4*m, "Deployment"), at(5*m, "Service"), at(30*m, "Secret")}}, none, "Deployment-4m0s", false},
		{"within the slop counts as before", issuesapi.Issue{FirstSeen: firstSeen, CorrelatedChanges: []issuesapi.RecentChange{
			at(-20*m, "ConfigMap"), at(20*time.Second, "Deployment")}}, none, "Deployment20s", false},
		{"never a change after first seen", issuesapi.Issue{FirstSeen: firstSeen, CorrelatedChanges: []issuesapi.RecentChange{
			at(2*m, "Deployment"), at(40*m, "ConfigMap")}}, none, "", true},
		{"no first seen: the newest in the window", issuesapi.Issue{OnsetUnknown: true, CorrelatedChanges: []issuesapi.RecentChange{
			at(-20*m, "ConfigMap"), at(40*m, "Deployment"), at(5*m, "Service")}}, none, "Deployment40m0s", false},
		{"no changes", issuesapi.Issue{FirstSeen: firstSeen}, none, "", true},
		{"a source Radar couldn't read may hold a newer change", issuesapi.Issue{FirstSeen: firstSeen,
			Correlation:       &issuesapi.ChangeCorrelation{Unknown: &issuesapi.CorrelationUnknown{Reason: issuesapi.CorrelationUnknownReadError}},
			CorrelatedChanges: []issuesapi.RecentChange{at(-4*m, "Deployment")}}, none, "", true},
		{"a source the caller can't read may hold a newer change", issuesapi.Issue{FirstSeen: firstSeen,
			Correlation:       &issuesapi.ChangeCorrelation{Unknown: &issuesapi.CorrelationUnknown{Reason: issuesapi.CorrelationUnknownUserForbidden}},
			CorrelatedChanges: []issuesapi.RecentChange{at(-4*m, "Deployment")}}, none, "", true},
		{"missing history ends before the pick", historyGap,
			issuesapi.HistoryCoverage{Incomplete: true, CompleteSince: firstSeen.Add(-10 * m)}, "Deployment-4m0s", false},
		{"missing history may hold a newer change than the pick", historyGap,
			issuesapi.HistoryCoverage{Incomplete: true, CompleteSince: firstSeen.Add(-2 * m)}, "", true},
		{"missing history with no known boundary", historyGap, issuesapi.HistoryCoverage{Incomplete: true}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RowChange(tc.iss, tc.history)
			if ok == tc.wantNot || got.EventID != tc.want {
				t.Fatalf("got %q (ok %v), want %q", got.EventID, ok, tc.want)
			}
			if ok && (got.Fields[0].OldValue != nil || got.Fields[0].NewValue != nil || got.Fields[0].Path == "") {
				t.Fatalf("row kept field values: %+v", got.Fields)
			}
		})
	}
}

// A source whose watch started after the subject's can hide a change newer
// than the subject's recorded one, so a pick older than the merged history
// boundary is not named; a pick after it is.
func TestRowChange_SourceWatchedLaterThanSubject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		at     func(from, boundary time.Time) time.Time
		wantOK bool
	}{
		{"pick before the ConfigMap watch began", func(from, boundary time.Time) time.Time { return from.Add(boundary.Sub(from) / 2) }, false},
		{"pick after the ConfigMap watch began", func(_, boundary time.Time) time.Time { return boundary.Add(2 * time.Second) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, boundary := initDeferredCache(t, k8score.ConfigMaps, webDeployment())
			store := initStore(t)
			appendChange(t, store, specUpdate("Deployment", "web", tc.at(from, boundary), "web:bad"))
			iss := webIssue(time.Time{})
			got := Correlate(context.Background(), iss, Options{Window: Window{From: from, To: time.Now().Add(time.Minute)},
				Selection: SelectAroundFirstSeen, CheckCoverage: true, EventIDs: true}, Access{})
			if len(got.Changes) != 1 || got.Unknown == nil || got.Unknown.Reason != issuesapi.CorrelationUnknownHistoryIncomplete {
				t.Fatalf("setup: changes=%d unknown=%+v", len(got.Changes), got.Unknown)
			}
			iss.CorrelatedChanges = got.Changes
			iss.Correlation = &issuesapi.ChangeCorrelation{Unknown: got.Unknown}
			if _, ok := RowChange(iss, got.History); ok != tc.wantOK {
				t.Fatalf("row change named: %v, want %v (boundary %v, change %s)", ok, tc.wantOK, boundary, got.Changes[0].Timestamp)
			}
		})
	}
}
