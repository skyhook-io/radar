package prometheus

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes registers Prometheus metric routes on the given router.
func RegisterRoutes(r chi.Router) {
	r.Get("/prometheus/status", handleStatus)
	r.Post("/prometheus/connect", handleConnect)
	r.Get("/prometheus/resources/{kind}/{namespace}/{name}", handleResourceMetrics)
	r.Get("/prometheus/resources/{kind}/{name}", handleClusterScopedResourceMetrics)
	r.Get("/prometheus/namespace/{namespace}", handleNamespaceMetrics)
	r.Get("/prometheus/cluster", handleClusterMetrics)
	r.Get("/prometheus/query", handleRawQuery)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[prometheus] Failed to encode JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleStatus returns the current Prometheus connection status.
func handleStatus(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, Status{Available: false, Error: "Prometheus client not initialized"})
		return
	}
	writeJSON(w, http.StatusOK, client.GetStatus())
}

// handleConnect triggers Prometheus discovery and connection.
// Accepts optional "url" query param to override discovery with a specific endpoint.
func handleConnect(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Prometheus client not initialized")
		return
	}

	// Allow URL override via query param (resets existing connection)
	if overrideURL := r.URL.Query().Get("url"); overrideURL != "" {
		u, err := url.Parse(overrideURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			writeError(w, http.StatusBadRequest, "invalid URL: must be a valid HTTP(S) URL")
			return
		}
		client.SetURL(overrideURL)
	}

	_, _, err := client.EnsureConnected(r.Context())
	if err != nil {
		log.Printf("[prometheus] Connection failed: %v", err)
		writeError(w, http.StatusBadGateway, "Prometheus connection failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, client.GetStatus())
}

// parseTimeRange parses the "range" query parameter into start/end/step.
// Supported values: 10m, 30m, 1h, 3h, 6h, 12h, 24h, 48h, 7d, 14d (default: 1h).
// The frontend UI exposes a subset of these; the full set is available via the API.
func parseTimeRange(rangeStr string) (start, end time.Time, step time.Duration) {
	end = time.Now()

	var duration time.Duration
	switch rangeStr {
	case "10m":
		duration = 10 * time.Minute
		step = 15 * time.Second
	case "30m":
		duration = 30 * time.Minute
		step = 30 * time.Second
	case "1h", "":
		duration = time.Hour
		step = time.Minute
	case "3h":
		duration = 3 * time.Hour
		step = 2 * time.Minute
	case "6h":
		duration = 6 * time.Hour
		step = 5 * time.Minute
	case "12h":
		duration = 12 * time.Hour
		step = 10 * time.Minute
	case "24h":
		duration = 24 * time.Hour
		step = 15 * time.Minute
	case "48h":
		duration = 48 * time.Hour
		step = 30 * time.Minute
	case "7d":
		duration = 7 * 24 * time.Hour
		step = time.Hour
	case "14d":
		duration = 14 * 24 * time.Hour
		step = 2 * time.Hour
	default:
		log.Printf("[prometheus] Unrecognized range %q, falling back to 1h", rangeStr)
		rangeStr = "1h"
		duration = time.Hour
		step = time.Minute
	}

	start = end.Add(-duration)
	return
}

// ResourceMetricsResponse is the response shape for resource metrics.
type ResourceMetricsResponse struct {
	Kind      string         `json:"kind"`
	Namespace string         `json:"namespace,omitempty"`
	Name      string         `json:"name"`
	Category  MetricCategory `json:"category"`
	Unit      string         `json:"unit"`
	Range     string         `json:"range"`
	Result    *QueryResult   `json:"result"`
}

// handleResourceMetrics returns Prometheus metrics for a specific resource.
// Query params: category (cpu|memory|network_rx|network_tx|filesystem, default: cpu), range (10m|30m|1h|...|14d, default: 1h)
func handleResourceMetrics(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Prometheus client not initialized")
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	category := MetricCategory(r.URL.Query().Get("category"))
	if category == "" {
		category = CategoryCPU
	}

	// Validate kind is supported
	supported := false
	for _, k := range SupportedKinds() {
		if strings.EqualFold(k, kind) {
			kind = k // normalize casing
			supported = true
			break
		}
	}
	if !supported {
		writeError(w, http.StatusBadRequest, "unsupported resource kind: "+kind)
		return
	}

	// Validate category
	validCategories := CategoriesForKind(kind)
	categoryValid := false
	for _, c := range validCategories {
		if c == category {
			categoryValid = true
			break
		}
	}
	if !categoryValid {
		writeError(w, http.StatusBadRequest, "unsupported metric category for "+kind+": "+string(category))
		return
	}

	query := BuildQuery(kind, namespace, name, category)
	if query == "" {
		writeError(w, http.StatusBadRequest, "cannot build query for "+kind+"/"+string(category))
		return
	}

	rangeStr := r.URL.Query().Get("range")
	start, end, step := parseTimeRange(rangeStr)

	result, err := client.QueryRange(r.Context(), query, start, end, step)
	if err != nil {
		log.Printf("[prometheus] Query failed for %s/%s/%s (%s): %v", kind, namespace, name, category, err)
		writeError(w, http.StatusBadGateway, "Prometheus query failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ResourceMetricsResponse{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		Category:  category,
		Unit:      CategoryUnitForKind(kind, category),
		Range:     rangeStr,
		Result:    result,
	})
}

// handleClusterScopedResourceMetrics handles metrics for cluster-scoped resources (e.g. Node).
func handleClusterScopedResourceMetrics(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Prometheus client not initialized")
		return
	}

	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")

	// Only Node is a cluster-scoped kind with metrics
	if !strings.EqualFold(kind, "Node") {
		writeError(w, http.StatusBadRequest, "unsupported cluster-scoped resource kind: "+kind)
		return
	}
	kind = "Node"

	category := MetricCategory(r.URL.Query().Get("category"))
	if category == "" {
		category = CategoryCPU
	}

	validCategories := CategoriesForKind(kind)
	categoryValid := false
	for _, c := range validCategories {
		if c == category {
			categoryValid = true
			break
		}
	}
	if !categoryValid {
		writeError(w, http.StatusBadRequest, "unsupported metric category for "+kind+": "+string(category))
		return
	}

	query := BuildQuery(kind, "", name, category)
	if query == "" {
		writeError(w, http.StatusBadRequest, "cannot build query for "+kind+"/"+string(category))
		return
	}

	rangeStr := r.URL.Query().Get("range")
	start, end, step := parseTimeRange(rangeStr)

	result, err := client.QueryRange(r.Context(), query, start, end, step)
	if err != nil {
		log.Printf("[prometheus] Query failed for %s/%s (%s): %v", kind, name, category, err)
		writeError(w, http.StatusBadGateway, "Prometheus query failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ResourceMetricsResponse{
		Kind:     kind,
		Name:     name,
		Category: category,
		Unit:     CategoryUnitForKind(kind, category),
		Range:    rangeStr,
		Result:   result,
	})
}

// NamespaceMetricsResponse is the response shape for namespace-level metrics.
type NamespaceMetricsResponse struct {
	Namespace string         `json:"namespace"`
	Category  MetricCategory `json:"category"`
	Unit      string         `json:"unit"`
	Range     string         `json:"range"`
	Result    *QueryResult   `json:"result"`
}

// handleNamespaceMetrics returns aggregate metrics for a namespace.
func handleNamespaceMetrics(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Prometheus client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	category := MetricCategory(r.URL.Query().Get("category"))
	if category == "" {
		category = CategoryCPU
	}

	query := BuildNamespaceQuery(namespace, category)
	if query == "" {
		writeError(w, http.StatusBadRequest, "unsupported category for namespace: "+string(category))
		return
	}

	rangeStr := r.URL.Query().Get("range")
	start, end, step := parseTimeRange(rangeStr)

	result, err := client.QueryRange(r.Context(), query, start, end, step)
	if err != nil {
		log.Printf("[prometheus] Namespace query failed for %s (%s): %v", namespace, category, err)
		writeError(w, http.StatusBadGateway, "Prometheus query failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, NamespaceMetricsResponse{
		Namespace: namespace,
		Category:  category,
		Unit:      CategoryUnit(category),
		Range:     rangeStr,
		Result:    result,
	})
}

// ClusterMetricsResponse is the response shape for cluster-level metrics.
type ClusterMetricsResponse struct {
	Category MetricCategory `json:"category"`
	Unit     string         `json:"unit"`
	Range    string         `json:"range"`
	Result   *QueryResult   `json:"result"`
}

// handleClusterMetrics returns aggregate metrics for the entire cluster.
func handleClusterMetrics(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Prometheus client not initialized")
		return
	}

	category := MetricCategory(r.URL.Query().Get("category"))
	if category == "" {
		category = CategoryCPU
	}

	query := BuildClusterQuery(category)
	if query == "" {
		writeError(w, http.StatusBadRequest, "unsupported category for cluster: "+string(category))
		return
	}

	rangeStr := r.URL.Query().Get("range")
	start, end, step := parseTimeRange(rangeStr)

	result, err := client.QueryRange(r.Context(), query, start, end, step)
	if err != nil {
		log.Printf("[prometheus] Cluster query failed (%s): %v", category, err)
		writeError(w, http.StatusBadGateway, "Prometheus query failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ClusterMetricsResponse{
		Category: category,
		Unit:     CategoryUnit(category),
		Range:    rangeStr,
		Result:   result,
	})
}

// handleRawQuery proxies a raw PromQL query to Prometheus.
// Query params: query (PromQL), range (time range), type (instant|range)
func handleRawQuery(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Prometheus client not initialized")
		return
	}

	query := r.URL.Query().Get("query")
	if query == "" {
		writeError(w, http.StatusBadRequest, "query parameter is required")
		return
	}

	queryType := r.URL.Query().Get("type")
	if queryType == "instant" {
		result, err := client.Query(r.Context(), query)
		if err != nil {
			log.Printf("[prometheus] Raw instant query failed: %v", err)
			writeError(w, http.StatusBadGateway, "Prometheus query failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}

	// Default to range query
	rangeStr := r.URL.Query().Get("range")
	start, end, step := parseTimeRange(rangeStr)

	result, err := client.QueryRange(r.Context(), query, start, end, step)
	if err != nil {
		log.Printf("[prometheus] Raw range query failed: %v", err)
		writeError(w, http.StatusBadGateway, "Prometheus query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
