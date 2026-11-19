package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/issuecorrelation"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// maxIssueRecentChanges bounds one batched request: about one screen of
// issue rows. Each distinct subject costs one per-resource read.
const maxIssueRecentChanges = 50

// IssueRecentChangesResponse is GET /api/issues/recent-changes.
type IssueRecentChangesResponse struct {
	// Changes holds, per issue ID, the one change that issue's row names.
	// An issue with no such change is absent, whatever the reason: no
	// change, Radar can't tell (including an answer partly unknown, which
	// may hide a newer change), an issue that isn't correlated or is no
	// longer there, or a subject the caller may not read. Absence is
	// therefore never "nothing changed".
	Changes map[string]IssueRowChange `json:"changes"`
}

// IssueRowChange is the change one issue row names, with the occurrence of
// the issue it was read for. An issue's ID survives resolution and
// recurrence, so a client compares first_seen and resource_created_at with
// its own copy and drops an answer for another occurrence.
type IssueRowChange struct {
	FirstSeen         time.Time              `json:"first_seen,omitzero"`
	ResourceCreatedAt time.Time              `json:"resource_created_at,omitzero"`
	Change            issuesapi.RecentChange `json:"change"`
}

// handleIssueRecentChanges serves GET /api/issues/recent-changes, the batched
// lookup behind collapsed issue rows: for each listed issue, the newest change
// at or before it was first seen (or the newest in the window when it has no
// first-seen time).
//
// Each repeated issue= names one issue and its subject in the per-resource
// route's terms: <id>/<kind>/<namespace>/<name>[/<group>], with "_" for a
// cluster-scoped subject's namespace. Every subject is read exactly as
// GET /api/issues/resource/{kind}/{namespace}/{name}?correlate=<id> reads it
// (the drawer's preflight, the per-row gates, the same window and selection),
// so an issue the caller couldn't correlate there gets nothing here, and a
// subject the caller may not read is indistinguishable from one with no
// change. It stays off /api/issues, which several screens poll.
func (s *Server) handleIssueRecentChanges(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	raw := r.URL.Query()["issue"]
	if len(raw) == 0 {
		s.writeError(w, http.StatusBadRequest, "issue is required: <id>/<kind>/<namespace>/<name>[/<group>]")
		return
	}
	if len(raw) > maxIssueRecentChanges {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d issues per request, got %d", maxIssueRecentChanges, len(raw)))
		return
	}
	var order []resourceIssuesTarget
	ids := map[resourceIssuesTarget][]string{}
	for _, value := range raw {
		id, target, err := parseIssueRowRef(value)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, seen := ids[target]; !seen {
			order = append(order, target)
		}
		if !slices.Contains(ids[target], id) {
			ids[target] = append(ids[target], id)
		}
	}

	resp := IssueRecentChangesResponse{Changes: map[string]IssueRowChange{}}
	for _, target := range order {
		read, err := s.readResourceIssues(r, provider, target, ids[target])
		if err != nil {
			if connectionChanged(err) {
				s.writeCollectionFailure(w, read.kind, errReadHandlesChanged)
				return
			}
			// Anything else the per-resource route would refuse leaves this
			// subject's rows without a change.
			continue
		}
		for _, id := range ids[target] {
			if read.correlationStatus[id] != "" {
				continue
			}
			for _, iss := range read.related {
				if iss.ID != id {
					continue
				}
				if change, ok := issuecorrelation.RowChange(iss, read.correlationHistory[id]); ok {
					resp.Changes[id] = IssueRowChange{FirstSeen: iss.FirstSeen, ResourceCreatedAt: iss.ResourceCreatedAt, Change: change}
				}
				break
			}
		}
	}
	s.writeJSON(w, resp)
}

// parseIssueRowRef reads <id>/<kind>/<namespace>/<name>[/<group>].
func parseIssueRowRef(value string) (string, resourceIssuesTarget, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 4 && len(parts) != 5 {
		return "", resourceIssuesTarget{}, fmt.Errorf("issue %q: want <id>/<kind>/<namespace>/<name>[/<group>]", value)
	}
	for _, part := range parts[:4] {
		if part == "" {
			return "", resourceIssuesTarget{}, fmt.Errorf("issue %q: id, kind, namespace and name must be set (\"_\" for a cluster-scoped namespace)", value)
		}
	}
	target := resourceIssuesTarget{rawKind: parts[1], namespace: parts[2], name: parts[3]}
	if target.namespace == "_" {
		target.namespace = ""
	}
	if len(parts) == 5 {
		target.group = parts[4]
	}
	return parts[0], target, nil
}

func connectionChanged(err error) bool {
	var failure *resourceReadFailure
	return errors.Is(err, errReadHandlesChanged) || errors.As(err, &failure) && failure.code == readreason.ConnectionChanged
}
