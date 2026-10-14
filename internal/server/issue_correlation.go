package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// maxCorrelationSubjects bounds one lookup: every subject costs timeline
// queries, and a client only needs the rows it is showing.
const maxCorrelationSubjects = 50

// handleIssueCorrelation serves GET /api/issues/correlation — per-issue change
// correlation for the subjects a client is showing, each passed as
// ?subject=kind/group/namespace/name (group and namespace may be empty). It is
// its own lookup rather than part of /api/issues because Home and Applications
// poll that endpoint, and only rows someone is looking at should pay for the
// timeline queries. It is a GET because it only reads: a proxy in front of
// Radar may authorize any other method as a write. The client bounds the batch,
// so there is no single-namespace rule or issue cap as in the MCP issues tool;
// each subject is authorized on its own.
func (s *Server) handleIssueCorrelation(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	raw := r.URL.Query()["subject"]
	if len(raw) > maxCorrelationSubjects {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d subjects per request", maxCorrelationSubjects))
		return
	}
	subjects := make([]issuesapi.IssueCorrelationSubject, 0, len(raw))
	for _, v := range raw {
		subj, ok := parseCorrelationSubject(v)
		if !ok {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("subject %q must be kind/group/namespace/name with a kind and a name", v))
			return
		}
		subjects = append(subjects, subj)
	}

	window, windowUnknown := meaningfulchanges.CorrelationWindow()
	src := meaningfulchanges.CorrelationSources{
		Visible:     s.filterRecentChangesByRBAC,
		HelmChanges: s.helmChangesForCorrelation,
	}
	resp := issuesapi.IssueCorrelationResponse{Results: make([]issuesapi.IssueCorrelation, 0, len(subjects))}
	for _, subj := range subjects {
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

func parseCorrelationSubject(v string) (issuesapi.IssueCorrelationSubject, bool) {
	parts := strings.Split(v, "/")
	if len(parts) != 4 || parts[0] == "" || parts[3] == "" {
		return issuesapi.IssueCorrelationSubject{}, false
	}
	return issuesapi.IssueCorrelationSubject{Kind: parts[0], Group: parts[1], Namespace: parts[2], Name: parts[3]}, true
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
