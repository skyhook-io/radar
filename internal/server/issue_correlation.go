package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	// maxCorrelationSubjects bounds one lookup: every subject costs timeline
	// queries, and a client only needs the rows it is showing.
	maxCorrelationSubjects  = 50
	maxCorrelationBodyBytes = 64 << 10
)

// handleIssueCorrelation serves POST /api/issues/correlation — per-issue change
// correlation for the subjects a client is showing. It is its own lookup rather
// than part of /api/issues because Home and Applications poll that endpoint,
// and only rows someone is looking at should pay for the timeline queries. The
// client bounds the batch, so there is no single-namespace rule or issue cap as
// in the MCP issues tool; each subject is authorized on its own.
func (s *Server) handleIssueCorrelation(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	var req issuesapi.IssueCorrelationRequest
	if err := decodeBoundedJSONBody(w, r, maxCorrelationBodyBytes, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %d KiB", maxCorrelationBodyBytes>>10))
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid correlation request: "+err.Error())
		return
	}
	if len(req.Subjects) > maxCorrelationSubjects {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d subjects per request", maxCorrelationSubjects))
		return
	}
	for _, subj := range req.Subjects {
		if subj.Kind == "" || subj.Name == "" {
			s.writeError(w, http.StatusBadRequest, "every subject needs a kind and a name")
			return
		}
	}

	window, windowUnknown := meaningfulchanges.CorrelationWindow()
	src := meaningfulchanges.CorrelationSources{
		Visible:     s.filterRecentChangesByRBAC,
		HelmChanges: s.helmChangesForCorrelation,
	}
	resp := issuesapi.IssueCorrelationResponse{Results: make([]issuesapi.IssueCorrelation, 0, len(req.Subjects))}
	for _, subj := range req.Subjects {
		result := issuesapi.IssueCorrelation{IssueCorrelationSubject: subj}
		switch {
		case !meaningfulchanges.CorrelationEligible(subj.Kind, subj.Group):
			result.UnknownReason = issuesapi.CorrelationUntrackedKind
		case !s.canCorrelateSubject(r, subj):
			result.UnknownReason = issuesapi.CorrelationNotPermitted
		case window == 0:
			result.UnknownReason = windowUnknown
		default:
			c := meaningfulchanges.CorrelateSubject(r.Context(), subj.Kind, subj.Group, subj.Namespace, subj.Name, window, src)
			result.CorrelatedChanges = c.Changes
			result.NoRecentChanges = c.NoRecentChanges
			result.UnknownReason = c.Unknown
		}
		resp.Results = append(resp.Results, result)
	}
	s.writeJSON(w, resp)
}

// canCorrelateSubject requires the caller to be able to list the subject's own
// kind where it lives: a no_recent_changes answer is a statement about that
// resource's history. Native Helm releases are authorized when their history is
// read as the caller.
func (s *Server) canCorrelateSubject(r *http.Request, subj issuesapi.IssueCorrelationSubject) bool {
	if subj.Namespace != "" && k8s.ForceNamespaceScope && subj.Namespace != k8s.GetNamespaceScopeTarget() {
		return false
	}
	if meaningfulchanges.IsNativeHelmSubject(subj.Kind, subj.Group) {
		return subj.Namespace != ""
	}
	group, resource, clusterScoped, ok := k8s.ResolveChangeGVR(subj.Kind, subj.Group)
	if !ok {
		return false
	}
	if clusterScoped {
		return s.canRead(r, group, resource, "", "list")
	}
	if subj.Namespace == "" || noNamespaceAccess(s.getUserNamespaces(r, []string{subj.Namespace})) {
		return false
	}
	return s.canRead(r, group, resource, subj.Namespace, "list")
}

// helmChangesForCorrelation reads one native release's history as the caller,
// so Helm's own authorization decides what is visible.
func (s *Server) helmChangesForCorrelation(ctx context.Context, namespace, name string, window time.Duration) ([]issuesapi.RecentChange, error) {
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil, errors.New("helm client not available")
	}
	username, groups := "", []string(nil)
	if user := auth.UserFromContext(ctx); user != nil {
		username, groups = user.Username, user.Groups
	}
	releases, err := helmClient.ListReleasesAcrossNamespaces([]string{namespace}, username, groups)
	if err != nil {
		if helm.IsForbiddenError(err) {
			return nil, fmt.Errorf("%w: %v", meaningfulchanges.ErrCorrelationNotPermitted, err)
		}
		return nil, err
	}
	return meaningfulchanges.HelmRecentChanges(releases, name, window, time.Now()), nil
}
