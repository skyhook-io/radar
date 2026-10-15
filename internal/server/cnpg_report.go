package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/version"
)

// The Cluster report bundle mirrors `kubectl cnpg report cluster`: the
// Cluster, its Pods, Jobs, PVCs and events as manifests, optionally the Pods'
// logs. Radar adds what it already knows (Backups, ScheduledBackups, Poolers,
// the ObjectStore, operator and plugin versions, the runtime and storage
// snapshots) and a coverage record of everything it could not read and why.
// Secret values are never read; the bundle lists the Secrets the cluster
// references by name. Logs, and the query text inside them, are opt-in.

func parseCNPGReportOptions(r *http.Request) (cnpgsvc.ReportOptions, error) {
	q := r.URL.Query()
	opts := cnpgsvc.ReportOptions{Logs: q.Get("logs") == "true", QueryText: q.Get("queryText") == "true", TailLines: cnpgsvc.ReportDefaultTail}
	if v := q.Get("tailLines"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 || n > cnpgsvc.ReportMaxTail {
			return opts, fmt.Errorf("tailLines must be between 1 and %d", cnpgsvc.ReportMaxTail)
		}
		opts.TailLines = n
	}
	if opts.QueryText && !opts.Logs {
		return opts, errors.New("queryText applies to logs; set logs=true as well")
	}
	if !opts.Logs {
		opts.TailLines = 0
	}
	return opts, nil
}

// handleCNPGClusterReport serves GET /api/cnpg/clusters/{ns}/{name}/report as a
// zip download. Only the Cluster read itself can fail the request; every other
// read is skipped and recorded in report.json.
// Direct reads use the caller's impersonated clients; the namespace sentinel
// applies only to the shared-cache snapshots added to the bundle.
func (s *Server) handleCNPGClusterReport(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	opts, err := parseCNPGReportOptions(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	reader := s.cnpgReader(r)
	dyn, contextName, typed := reader.dynamic, reader.actionContext, reader.Clients.Typed
	if dyn == nil || typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cnpgsvc.ReportTimeout)
	defer cancel()
	cluster, err := dyn.Resource(cnpgsvc.ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		s.writeCNPGReadError(w, err, cnpgsvc.GrantGetCluster, namespace, name)
		return
	}
	auth.AuditLog(r, namespace, name)

	meta := cnpgsvc.ReportMetadata{Context: contextName, RadarVersion: version.Current, Now: time.Now().UTC()}
	if user := auth.UserFromContext(r.Context()); user != nil {
		meta.RequestedBy = user.Username
	}
	data, root, err := reader.Report(ctx, dyn, typed, cluster, opts, meta)
	if err != nil {
		log.Printf("[cnpg] Failed to build report for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, "failed to build the report")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, root))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		log.Printf("[cnpg] Failed to send report for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
	}
}
