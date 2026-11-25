package server

import (
	"context"
	"net/http"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issuecorrelation"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// correlationStatusNotFound says the issue named by ?correlate= is not in the
// answer: it resolved, or it is outside what the caller may see. The per-row
// gates already ran, so this never says whether a hidden issue exists.
const correlationStatusNotFound = "not_found"

// correlationAccess reads as the request's caller. Every permission check
// keeps whether it was answered, so a failed check is told apart from a
// denial. helmReleases are the release rows the request already read as the
// caller; a native Helm release's history comes from them.
func (s *Server) correlationAccess(r *http.Request, helmReleases []helm.HelmRelease) issuecorrelation.Access {
	ctx := r.Context()
	access := issuecorrelation.Access{
		HelmHistory: func(_ context.Context, _, name string, window issuecorrelation.Window) ([]issuesapi.RecentChange, error) {
			return issuecorrelation.HelmRecentChanges(helmReleases, name, window), nil
		},
	}
	if auth.UserFromContext(ctx) == nil {
		return access
	}
	access.Check = func(kind, group, namespace string) issuecorrelation.Decision {
		resolvedGroup, resource, clusterScoped, ok := k8s.ResolveChangeGVR(kind, group)
		if !ok {
			return issuecorrelation.CheckFailed
		}
		if clusterScoped {
			namespace = ""
		}
		allowed, answered := s.canReadDecision(r, resolvedGroup, resource, namespace, "list")
		switch {
		case allowed:
			return issuecorrelation.Allowed
		case answered:
			return issuecorrelation.Forbidden
		}
		return issuecorrelation.CheckFailed
	}
	access.RowFilter = s.recentChangeReadFilter(ctx)
	access.Viewer = s.recentChangeViewer(ctx)
	return access
}

// correlateResourceIssue correlates the issue with id in place and returns
// the envelope's correlation_status: empty when it was found. history is the
// correlation's coverage before it is filtered for the caller, for the row
// pick (issuecorrelation.RowChange); it never leaves the server. An error
// means the timeline store or cluster connection changed during the read.
func (s *Server) correlateResourceIssue(r *http.Request, related []issues.Issue, id string, helmReleases []helm.HelmRelease) (status string, history issuesapi.HistoryCoverage, err error) {
	index := -1
	for i := range related {
		if related[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return correlationStatusNotFound, history, nil
	}
	iss := &related[index]
	now := time.Now()
	window := issuecorrelation.WindowAroundFirstSeen(*iss, now)
	opts := issuecorrelation.Options{Window: window, Selection: issuecorrelation.SelectAroundFirstSeen, CheckCoverage: true, EventIDs: true}
	access := s.correlationAccess(r, helmReleases)
	var result issuecorrelation.Result
	if err := withTimelineIdentity(func() {
		result = issuecorrelation.Correlate(r.Context(), *iss, opts, access)
	}); err != nil {
		return "", history, err
	}
	if !result.Eligible {
		return "", history, nil
	}
	if result.Timing.Changed {
		iss.IssueTiming, iss.IssueTimingBasis, iss.TimingSummary = result.Timing.IssueTiming, result.Timing.IssueTimingBasis, result.Timing.TimingSummary
	}
	iss.CorrelatedChanges, iss.CorrelatedChangesIncomplete, iss.CorrelatedChangesMoreNotShown = nil, false, false
	iss.NoRecentChanges = nil
	// Incomplete is sent with an empty list too: a read that hit its bound
	// and found nothing is not a complete empty read.
	iss.CorrelatedChangesIncomplete = result.FetchSaturated
	if len(result.Changes) > 0 {
		iss.CorrelatedChanges = result.Changes
		iss.CorrelatedChangesMoreNotShown = result.MoreNotShown
	} else if result.NoChanges {
		iss.NoRecentChanges = &issuesapi.NoRecentChangesMarker{WindowSeconds: int(window.To.Sub(window.From).Seconds())}
		if result.PolicyOnly {
			iss.NoRecentChanges.Scope = issuesapi.NoRecentChangesScopeAdmissionPolicy
		}
	}
	iss.Correlation = &issuesapi.ChangeCorrelation{
		WindowFrom:    window.From.UTC(),
		WindowTo:      window.To.UTC(),
		ObservedSince: timeline.ObservationStart().UTC(),
		Unknown:       result.Unknown,
		History:       meaningfulchanges.HistoryCoverageFor(result.History, s.callerReadsAllNamespaces(r)),
	}
	return "", result.History, nil
}

// helmCorrelationStatus is the envelope's status when the issue named by
// ?correlate= is a native Helm release whose storage the caller's read could
// not open: the issue can't be composed, so the answer is unknown rather
// than gone.
func helmCorrelationStatus(unchecked issuesapi.UncheckedSource) string {
	if unchecked.Reason == issuesapi.UncheckedReasonCallerAccessDenied {
		return string(readreason.UserForbidden)
	}
	return string(readreason.ReadError)
}

// withTimelineIdentity runs read and fails when the timeline store or the
// cluster it records changed meanwhile: rows read across a context switch can
// belong to the previous cluster.
func withTimelineIdentity(read func()) error {
	store, cluster := timeline.GetStore(), k8s.ActiveClusterContext()
	read()
	if timeline.GetStore() != store || k8s.ActiveClusterContext() != cluster {
		return errReadHandlesChanged
	}
	return nil
}
