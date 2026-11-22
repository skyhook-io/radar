package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/vulnerabilities"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// handleVulnerabilities serves GET /api/vulnerabilities: scanner findings
// rolled up to the images that run now, by image, CVE, workload or fix.
//
// Query: view=image|cve|workload|fix|summary, severity=critical,high,
// fixable=1, exposed=1, q=, namespaces=, and workloadKind + workloadNamespace +
// workloadName (+ optional workloadGroup) to narrow to one workload (the
// workload drawer).
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
//
// Exposure (each row's workloads with a route from outside the cluster)
// reads Services and Ingresses in the answer's namespaces and Gateways and
// Gateway API routes across the caller's whole namespace ceiling, which
// namespace access covers for those kinds; the view's narrower picker
// scope would otherwise drop a shared Gateway and change the answer.
func (s *Server) handleVulnerabilities(w http.ResponseWriter, r *http.Request) {
	resp, ok := s.readVulnerabilities(w, r, false)
	if ok {
		s.writeJSON(w, resp)
	}
}

// handleVulnerabilitiesExport serves GET /api/vulnerabilities/export: one
// view (image, fix, cve or workload) as CSV, with the same query and gate as
// handleVulnerabilities, but listing every row and every value the list caps
// (CVE IDs, images, workloads), then rows for running images not scanned
// yet, images whose findings weren't read and coverage gaps. The findings
// budget (MaxFindings) still applies. Radar Cloud fans out to this route and
// stacks the files, so the columns are a contract (ExportColumns). One export
// runs at a time (409 for another), a file past 128 MB is refused
// with list_too_large rather than built, and a cancelled request stops
// building. The body
// is built whole and sent with its length (or gzipped, whose trailer does the
// same), so a cut transfer reads as an error, never as a shorter file.
func (s *Server) handleVulnerabilitiesExport(w http.ResponseWriter, r *http.Request) {
	if v := r.URL.Query().Get("view"); v == "" || v == string(vulnerabilities.ViewSummary) {
		s.writeError(w, http.StatusBadRequest, "view must be image, fix, cve or workload")
		return
	}
	// One export at a time: each builds its whole file in memory.
	select {
	case vulnerabilityExportSlot <- struct{}{}:
		defer func() { <-vulnerabilityExportSlot }()
	default:
		s.writeError(w, http.StatusConflict, "Another CSV export is running on this Radar. Try again in a moment.")
		return
	}
	// The server has no WriteTimeout: without a deadline of its own, a client
	// that stops reading would keep the only slot.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(vulnerabilityExportDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		log.Printf("[vulnerabilities] Failed to set the export's write deadline: %v", err)
	}
	// Reading and building stop at their own deadline, just inside the
	// route's 60s timeout, so a slow export gets a reason, not a bare 504.
	ctx, cancel := context.WithTimeout(r.Context(), vulnerabilityExportPrepareTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	timedOut := func() bool {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false
		}
		s.writeErrorCode(w, http.StatusServiceUnavailable, "export_timed_out", "This export took too long. Narrow it with the namespace picker or the filters, then download again.")
		return true
	}
	resp, ok := s.readVulnerabilities(w, r, true)
	if !ok {
		timedOut()
		return
	}
	body, err := vulnerabilities.WriteCSV(ctx, resp, vulnerabilityFilter(r), k8s.GetClusterName())
	switch {
	case ctx.Err() != nil:
		timedOut()
		return
	case errors.Is(err, vulnerabilities.ErrExportTooLarge):
		s.writeErrorCode(w, http.StatusBadRequest, string(readreason.ListTooLarge), exportTooLargeMessage)
		return
	case err != nil:
		log.Printf("[vulnerabilities] Failed to write the %s export: %v", resp.View, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

const exportTooLargeMessage = "This export is over 128 MB. Narrow it with the namespace picker or the filters, then download again."

// vulnerabilityExportSlot lets one CSV export run at a time on this Radar,
// for at most vulnerabilityExportDeadline, reading and delivery together.
var (
	vulnerabilityExportSlot     = make(chan struct{}, 1)
	vulnerabilityExportDeadline = 2 * time.Minute
	// vulnerabilityExportPrepareTimeout bounds reading and building the file;
	// past it the export answers 503 export_timed_out.
	vulnerabilityExportPrepareTimeout = 55 * time.Second
)

// vulnerabilityFilter is the request's filter; readVulnerabilities has
// already rejected a malformed one.
func vulnerabilityFilter(r *http.Request) vulnerabilities.Filter {
	q := r.URL.Query()
	severities, _ := vulnerabilities.ParseSeverities(q.Get("severity"))
	filter := vulnerabilities.Filter{
		Severities: severities, FixableOnly: q.Get("fixable") == "1" || q.Get("fixable") == "true", Query: q.Get("q"),
		ExposedOnly: q.Get("exposed") == "1" || q.Get("exposed") == "true",
	}
	if kind, ns, name := q.Get("workloadKind"), q.Get("workloadNamespace"), q.Get("workloadName"); kind != "" && ns != "" && name != "" {
		filter.Workload = &vulnerabilities.WorkloadRef{Kind: kind, Group: q.Get("workloadGroup"), Namespace: ns, Name: name}
	}
	return filter
}

// readVulnerabilities parses, gates and reads; on failure it has written the
// error and returns false.
func (s *Server) readVulnerabilities(w http.ResponseWriter, r *http.Request, export bool) (vulnerabilities.Response, bool) {
	if !s.requireConnected(w) {
		return vulnerabilities.Response{}, false
	}
	q := r.URL.Query()
	view, err := vulnerabilities.ParseView(q.Get("view"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return vulnerabilities.Response{}, false
	}
	if _, err := vulnerabilities.ParseSeverities(q.Get("severity")); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return vulnerabilities.Response{}, false
	}
	if kind, ns, name := q.Get("workloadKind"), q.Get("workloadNamespace"), q.Get("workloadName"); kind != "" || name != "" {
		if kind == "" || ns == "" || name == "" {
			s.writeError(w, http.StatusBadRequest, "workloadKind, workloadNamespace and workloadName go together")
			return vulnerabilities.Response{}, false
		}
	}

	namespaces, checkFailed := s.parseNamespacesForUserWithStatus(r)
	routeNamespaces, routeCheckFailed := s.routeNamespacesForUser(r)
	resp, err := vulnerabilities.Read(r.Context(), k8s.GetResourceCache(), vulnerabilities.Request{
		Namespaces:           namespaces,
		NamespaceCheckFailed: checkFailed,
		RouteNamespaces:      routeNamespaces,
		RouteCeiling:         !routeCheckFailed,
		ReportAccess:         s.searchKindAccess(r, s.computeSearchKindRBAC(r)),
		View:                 view,
		Filter:               vulnerabilityFilter(r),
		Export:               export,
	})
	if err != nil {
		if r.Context().Err() != nil {
			return vulnerabilities.Response{}, false
		}
		switch {
		case errors.Is(err, vulnerabilities.ErrNotInstalled):
			s.writeErrorCode(w, http.StatusNotFound, string(readreason.KindUnknown), err.Error())
		case errors.Is(err, vulnerabilities.ErrNotReady):
			s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.KindSyncPending), err.Error())
		case errors.Is(err, vulnerabilities.ErrConnectionChanged):
			s.writeErrorCode(w, http.StatusConflict, string(readreason.ConnectionChanged), err.Error())
		case errors.Is(err, vulnerabilities.ErrExportTooLarge):
			s.writeErrorCode(w, http.StatusBadRequest, string(readreason.ListTooLarge), exportTooLargeMessage)
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
		return vulnerabilities.Response{}, false
	}
	return resp, true
}

// routeNamespacesForUser is where the caller may read Gateways and Gateway
// API routes for exposure: their whole namespace ceiling, whatever the view
// is narrowed to, since a route and its Gateway often sit in another
// namespace than the workload they reach. failed is true when the ceiling
// couldn't be worked out; the read then uses the view's namespaces.
func (s *Server) routeNamespacesForUser(r *http.Request) (namespaces []string, failed bool) {
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		if target == "" {
			return []string{}, false
		}
		return []string{target}, false
	}
	return s.getUserNamespacesWithStatus(r, nil)
}

// isCollectorRefusal: Radar's own identity couldn't read the reports
// (forbidden, credentials rejected, not watchable). The caller's access isn't
// in question, so it isn't a 403.
func isCollectorRefusal(err error) bool {
	_, ok := readplan.CollectorErrorCode(err)
	return ok
}
