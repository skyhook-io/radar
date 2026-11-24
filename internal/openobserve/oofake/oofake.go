// Package oofake is a fake OpenObserve Prometheus API for tests.
package oofake

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

// Server answers the PromQL OpenObserve receives for clusters it knows: by
// radar_cluster_uid (ids) and Pods stored under cluster=<podCluster>.
type Server struct {
	IDs        map[string]string // kube-system uid → cluster name
	PodCluster string
	Pods       []oo.PodRef
	// FailStatus, when set, answers every request with it and FailBody.
	FailStatus int
	FailBody   string
	// Legacy makes the store ignore matchers on labels a metric's stream
	// lacks (OpenObserve before v1.2); LegacyUnscoped are the metrics whose
	// streams lack the cluster labels (their schema lists only cluster).
	Legacy         bool
	LegacyUnscoped []string
	// Undetectable leaves the filter-behaviour check without data, so the
	// version can't be told.
	Undetectable bool

	mu      sync.Mutex
	queries []string
	auth    []string
}

var (
	reUIDEq     = regexp.MustCompile(`radar_cluster_uid="([^"]+)"`)
	reClusterEq = regexp.MustCompile(`cluster="([^"]+)"`)
)

// Start serves f until the test ends.
func Start(t *testing.T, f *Server) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return srv
}

// Recorded returns every query received.
func (f *Server) Recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	q := r.Form.Get("query")
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if f.FailStatus != 0 {
		w.WriteHeader(f.FailStatus)
		_, _ = w.Write([]byte(f.FailBody))
		return
	}
	if m, ok := strings.CutPrefix(r.URL.Path, "/api/default/streams/"); ok && strings.HasSuffix(m, "/schema") {
		f.serveSchema(w, strings.TrimSuffix(m, "/schema"))
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/default/prometheus/api/v1/") {
		http.NotFound(w, r)
		return
	}
	var series []map[string]string
	switch {
	case f.Legacy && strings.Contains(q, "radar_probe_never_written"):
		series = append(series, map[string]string{})
	case q == "count(last_over_time(kube_pod_info[15m]))" && !f.Undetectable:
		// The filter-behaviour check's unfiltered read: data exists, so a
		// store that answered nothing for the never-written label filters.
		series = append(series, map[string]string{})
	case strings.Contains(q, "radar_probe_never_written"),
		strings.HasPrefix(q, "count by (k8s_cluster_uid"),
		strings.Contains(q, "up{radar_cluster_uid=~"):
	case strings.Contains(q, "kube_pod_info{radar_cluster_uid=~"):
		for uid, name := range f.IDs {
			if strings.Contains(q, uid) {
				series = append(series, map[string]string{"radar_cluster_uid": uid, "cluster": name})
			}
		}
	case strings.Contains(q, "kube_pod_info{namespace=") && strings.Contains(q, "uid=~"):
		for _, p := range f.Pods {
			if strings.Contains(q, p.UID) {
				series = append(series, map[string]string{"uid": p.UID, "cluster": f.PodCluster})
			}
		}
	case strings.Contains(q, "kube_pod_info{namespace="):
	case reUIDEq.MatchString(q):
		uid := reUIDEq.FindStringSubmatch(q)[1]
		if _, ok := f.IDs[uid]; ok {
			series = append(series, map[string]string{"radar_cluster_uid": uid}, map[string]string{"radar_cluster_uid": "someone-else"})
		}
	case reClusterEq.MatchString(q):
		if v := reClusterEq.FindStringSubmatch(q)[1]; v == f.PodCluster {
			series = append(series, map[string]string{"cluster": v})
		}
	case q == "vector(1)":
		series = append(series, map[string]string{})
	}
	WriteVector(w, series)
}

// SchemaQuery is how Recorded lists a stream schema lookup.
func SchemaQuery(metric string) string { return "schema:" + metric }

// serveSchema lists a stream's columns: a LegacyUnscoped metric has only a
// cluster column, any other every cluster label.
func (f *Server) serveSchema(w http.ResponseWriter, metric string) {
	f.mu.Lock()
	f.queries[len(f.queries)-1] = SchemaQuery(metric)
	f.mu.Unlock()
	cols := []string{"__name__", "_timestamp", "value", "cluster"}
	unscoped := false
	for _, m := range f.LegacyUnscoped {
		unscoped = unscoped || m == metric
	}
	if !unscoped {
		cols = append(cols, "radar_cluster_uid", "k8s_cluster", "k8s_cluster_uid")
	}
	schema := make([]map[string]string, 0, len(cols))
	for _, c := range cols {
		schema = append(schema, map[string]string{"name": c, "type": "Utf8"})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"name": metric, "stream_type": "metrics", "schema": schema})
}

// WriteVector writes an instant-vector answer with value 1 per series.
func WriteVector(w http.ResponseWriter, series []map[string]string) {
	result := make([]map[string]any, 0, len(series))
	for _, s := range series {
		result = append(result, map[string]any{"metric": s, "value": []any{1700000000, "1"}})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"data":   map[string]any{"resultType": "vector", "result": result},
	})
}
