package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/vulnerabilities"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/vulnintel"
)

// handleVulnerabilities serves GET /api/vulnerabilities: scanner findings
// rolled up to the images that run now, by image, CVE, workload or fix.
//
// Query: view=image|cve|workload|fix|summary, severity=critical,high,
// fixable=1, exposed=1, kev=1 (CISA Known Exploited only; 503
// kev_unavailable without the catalog, never an empty "none"), q=, namespaces=, and workloadKind + workloadNamespace +
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
// KEV and EPSS come from the process's feed store (--kev-feed,
// --epss-feed), loaded on the first read: public data, the same for every
// caller, fetched by Radar on its own schedule and never on a URL a request
// names. Response.intel says whether each feed is ready; until it is, the UI
// and CSV say "unavailable" rather than reading a missing entry as "no".
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
// yet, images whose findings weren't read and coverage gaps. view=findings,
// for exports only, writes one row per finding, image and workload
// container (a compliance tracker's shape), read as the image view. The findings
// budget (MaxFindings) still applies. Radar Cloud fans out to this route and
// stacks the files, so the columns are a contract (ExportColumns). One export
// runs at a time (409 for another), a file past 128 MB is refused
// with list_too_large rather than built, and a cancelled request stops
// building. The body
// is built whole and sent with its length (or gzipped, whose trailer does the
// same), so a cut transfer reads as an error, never as a shorter file.
func (s *Server) handleVulnerabilitiesExport(w http.ResponseWriter, r *http.Request) {
	if _, err := vulnerabilities.ParseExportView(r.URL.Query().Get("view")); err != nil || r.URL.Query().Get("view") == "" {
		s.writeError(w, http.StatusBadRequest, "view must be image, fix, cve, workload or findings")
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
		ExposedOnly:     q.Get("exposed") == "1" || q.Get("exposed") == "true",
		OSEndOfLifeOnly: q.Get("eol") == "1" || q.Get("eol") == "true",
		KEVOnly:         q.Get("kev") == "1" || q.Get("kev") == "true",
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
	parse := vulnerabilities.ParseView
	if export {
		parse = vulnerabilities.ParseExportView
	}
	view, err := parse(q.Get("view"))
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
		Intel:                vulnerabilities.IntelSnapshot(),
	})
	if err != nil {
		if r.Context().Err() != nil {
			return vulnerabilities.Response{}, false
		}
		switch {
		case errors.Is(err, vulnerabilities.ErrNotInstalled):
			s.writeErrorCode(w, http.StatusNotFound, string(readreason.KindUnknown), err.Error())
		case errors.Is(err, vulnerabilities.ErrKEVUnavailable):
			s.writeErrorCode(w, http.StatusServiceUnavailable, "kev_unavailable", err.Error())
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

// handleVulnerabilityFeedRefresh serves POST /api/vulnerabilities/feeds/refresh:
// reload CISA KEV and FIRST EPSS now rather than at the next daily load. It
// answers 202 with the feeds' state as it stands (still the old copy; the
// reload runs in the background and the next read shows it), 429 with
// Retry-After when anyone asked within the last 5 minutes, and 409
// feeds_off when both feeds are turned off.
//
// Gate: no Kubernetes access is involved. The feeds are one public copy per
// process, fetched from the operator's configured URLs or files (never a
// URL from the request), so any signed-in user may ask, and on a shared
// listener without authentication anyone who reaches Radar may too: the
// process-wide rate limit bounds it to one download of each feed per 5
// minutes. The same-origin middleware keeps a cross-site page from asking.
func (s *Server) handleVulnerabilityFeedRefresh(w http.ResponseWriter, r *http.Request) {
	wait, err := vulnerabilities.RefreshIntel()
	switch {
	case errors.Is(err, vulnintel.ErrFeedsOff):
		s.writeErrorCode(w, http.StatusConflict, "feeds_off", "The KEV and EPSS feeds are turned off on this Radar (--kev-feed and --epss-feed are off).")
		return
	case errors.Is(err, vulnintel.ErrRefreshTooSoon):
		secs := int(wait.Round(time.Second) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		s.writeErrorCode(w, http.StatusTooManyRequests, "refresh_too_soon", fmt.Sprintf("The feeds were refreshed less than 5 minutes ago. Try again in %s.", wait.Round(time.Second)))
		return
	case err != nil:
		log.Printf("[vulnerabilities] Failed to refresh the KEV and EPSS feeds: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"intel": vulnerabilities.IntelStatus()})
}
