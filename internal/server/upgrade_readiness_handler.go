package server

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/upgrade"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

// upgradeReadinessResponse wraps the scan with serving-layer freshness fields:
// observedAt is when the underlying scan ran (a memoized response keeps the
// original stamp, not the response time), and scanId identifies the snapshot
// so paging consumers can bind follow-up reads to it.
type upgradeReadinessResponse struct {
	*upgradereadiness.ScanResults
	ObservedAt time.Time `json:"observedAt"`
	ScanID     string    `json:"scanId"`
	FromCache  bool      `json:"fromCache,omitempty"`
}

func (s *Server) handleUpgradeReadiness(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	refresh := r.URL.Query().Get("refresh") == "true"
	outcome, err := upgrade.ScanMemoized(r.Context(), httpUpgradeAuthorizer{s: s, r: r}, r.URL.Query().Get("target"), refresh)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.writeUpgradeReadinessError(w, err)
		return
	}
	s.writeJSON(w, upgradeReadinessResponse{
		ScanResults: outcome.Results,
		ObservedAt:  outcome.ObservedAt,
		ScanID:      outcome.ScanID,
		FromCache:   outcome.FromCache,
	})
}

func (s *Server) writeUpgradeReadinessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, upgradereadiness.ErrInvalidTargetVersion), errors.Is(err, upgradereadiness.ErrNonForwardTarget):
		s.writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, upgradereadiness.ErrInvalidCurrentVersion):
		s.writeError(w, http.StatusServiceUnavailable, "Unable to determine the cluster Kubernetes version")
	case errors.Is(err, upgrade.ErrScanNotReady):
		s.writeError(w, http.StatusServiceUnavailable, "Cache not initialized")
	case errors.Is(err, upgrade.ErrScanStaleContext):
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		s.writeError(w, http.StatusInternalServerError, "Upgrade impact scan failed")
	}
}

// upgradeReadinessNamespaces intentionally ignores the active namespace picker:
// an upgrade affects the cluster, while the picker is only a browsing filter.
// Authenticated users are still limited to their full RBAC namespace ceiling,
// and --namespace-scope remains an explicit hard boundary on cached evidence.
func (s *Server) upgradeReadinessNamespaces(r *http.Request) []string {
	if k8s.ForceNamespaceScope {
		if namespace := k8s.GetNamespaceScopeTarget(); namespace != "" {
			return s.getUserNamespaces(r, []string{namespace})
		}
		return []string{}
	}
	return s.getUserNamespaces(r, nil)
}

func (s *Server) canReadSubresource(r *http.Request, group, resource, subresource, namespace, verb string) bool {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return true
	}
	client := k8s.GetClient()
	if client == nil {
		return false
	}
	allowed, err := auth.SubjectCanISubresource(r.Context(), client, user.Username, user.Groups, namespace, group, resource, subresource, verb)
	if err != nil {
		log.Printf("[upgrade-impact] authorization failed for %s on %s/%s: %v", verb, resource, subresource, err)
		return false
	}
	return allowed
}

// httpUpgradeAuthorizer adapts (*Server, *http.Request) to the upgrade
// evidence seam. Every method delegates to the exact helper the handler
// called before the extraction, so decisions are byte-for-byte identical.
type httpUpgradeAuthorizer struct {
	s *Server
	r *http.Request
}

func (a httpUpgradeAuthorizer) Namespaces() []string {
	return a.s.upgradeReadinessNamespaces(a.r)
}

func (a httpUpgradeAuthorizer) CanList(group, resource, namespace string) bool {
	return a.s.canRead(a.r, group, resource, namespace, "list")
}

func (a httpUpgradeAuthorizer) CanGetSubresource(group, resource, subresource string) bool {
	return a.s.canReadSubresource(a.r, group, resource, subresource, "", "get")
}

func (a httpUpgradeAuthorizer) FilterNamespacesByCanList(group, resource string, namespaces []string) []string {
	return a.s.filterNamespacesByCanRead(a.r, group, resource, "list", namespaces)
}
