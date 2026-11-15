package server

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/vulnerabilities"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// handleVulnerabilities serves GET /api/vulnerabilities: scanner findings
// rolled up to the images that run now, by image, CVE or workload.
//
// Query: view=image|cve|workload, severity=critical,high, fixable=1, q=,
// namespaces=, and workloadKind + workloadNamespace + workloadName to narrow
// to one workload (the workload drawer).
//
// Gate: the reports need the caller's own list permission on the report
// kind, per namespace, as every other Trivy read surface does (search,
// resource lists, counts): an ExposedSecretReport quotes the leaked secret,
// and being able to see a namespace doesn't mean being allowed to read its
// scan findings. Namespaces it withholds are coverage gaps, never silently
// empty. The Pods, ReplicaSets, Jobs and CronJobs the answer joins against
// are read in those same namespaces, which namespace access already covers
// for workload kinds (checkedInNamespace). With auth off it reads with
// Radar's identity, like the resource list; it is a GET, so the shared
// listener's cross-site read guard applies.
func (s *Server) handleVulnerabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	q := r.URL.Query()
	view, err := vulnerabilities.ParseView(q.Get("view"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	severities, err := vulnerabilities.ParseSeverities(q.Get("severity"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter := vulnerabilities.Filter{Severities: severities, FixableOnly: q.Get("fixable") == "1" || q.Get("fixable") == "true", Query: q.Get("q")}
	if kind, ns, name := q.Get("workloadKind"), q.Get("workloadNamespace"), q.Get("workloadName"); kind != "" || name != "" {
		if kind == "" || ns == "" || name == "" {
			s.writeError(w, http.StatusBadRequest, "workloadKind, workloadNamespace and workloadName go together")
			return
		}
		filter.Workload = &vulnerabilities.WorkloadRef{Kind: kind, Group: q.Get("workloadGroup"), Namespace: ns, Name: name}
	}

	namespaces, checkFailed := s.parseNamespacesForUserWithStatus(r)
	resp, err := vulnerabilities.Read(r.Context(), k8s.GetResourceCache(), vulnerabilities.Request{
		Namespaces:           namespaces,
		NamespaceCheckFailed: checkFailed,
		ReportAccess:         s.searchKindAccess(r, s.computeSearchKindRBAC(r)),
		View:                 view,
		Filter:               filter,
	})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		switch {
		case errors.Is(err, vulnerabilities.ErrNotInstalled):
			s.writeErrorCode(w, http.StatusNotFound, string(readreason.KindUnknown), err.Error())
		case errors.Is(err, vulnerabilities.ErrNotReady):
			s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.KindSyncPending), err.Error())
		case errors.Is(err, vulnerabilities.ErrConnectionChanged):
			s.writeErrorCode(w, http.StatusConflict, string(readreason.ConnectionChanged), err.Error())
		case isCollectorRefusal(err):
			code, _ := readplan.CollectorErrorCode(err)
			s.writeErrorCode(w, http.StatusServiceUnavailable, string(code), err.Error())
		default:
			scope := strings.Join(namespaces, ",")
			if namespaces == nil {
				scope = "*"
			}
			log.Printf("[vulnerabilities] Failed to read vulnerability reports %s/%s: %v", scope, "vulnerabilityreports", err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	s.writeJSON(w, resp)
}

// isCollectorRefusal: Radar's own identity couldn't read the reports
// (forbidden, credentials rejected, not watchable). The caller's access isn't
// in question, so it isn't a 403.
func isCollectorRefusal(err error) bool {
	_, ok := readplan.CollectorErrorCode(err)
	return ok
}
