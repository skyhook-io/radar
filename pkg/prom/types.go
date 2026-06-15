// Package prom provides a Prometheus HTTP API client with a pluggable
// Transport so the same query, parsing, and discovery logic can be used
// from any context that can reach a Prometheus endpoint — directly, via
// kubectl port-forward, or through a tunneled proxy.
//
// The package is intentionally pure: no global state, no singletons, no
// k8s client dependency in the Client itself. K8s-aware discovery is a
// separate step that constructs a Transport.
package prom

import (
	"encoding/json"
	"math"
	"strconv"
)

// ServiceInfo describes a Prometheus-compatible service discovered in the
// cluster. Used by discovery helpers and returned in Status.
type ServiceInfo struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      int    `json:"port"`
	BasePath  string `json:"basePath,omitempty"` // e.g. "/select/0/prometheus" for vmselect
}

// Status represents the current Prometheus connection status as exposed to
// callers/UI. Address is the effective URL (may be port-forwarded, a
// tunneled proxy URL, or a direct service URL depending on the Transport).
type Status struct {
	Available   bool         `json:"available"`
	Connected   bool         `json:"connected"`
	Address     string       `json:"address,omitempty"`
	Service     *ServiceInfo `json:"service,omitempty"`
	ContextName string       `json:"contextName,omitempty"`
	Error       string       `json:"error,omitempty"`
}

// QueryResult is the parsed result of a Prometheus query.
type QueryResult struct {
	ResultType string   `json:"resultType"`
	Series     []Series `json:"series"`
}

// Series is a single time series from a Prometheus query.
type Series struct {
	Labels     map[string]string `json:"labels"`
	DataPoints []DataPoint       `json:"dataPoints"`
}

// DataPoint is a single timestamped float sample.
type DataPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

// MarshalJSON emits a non-finite sample as an explicit null (not a fabricated 0)
// so consumers read it as a deliberate gap and skip it. Prometheus returns
// NaN/±Inf for ordinary expressions (0/0 error ratios, histogram_quantile on
// empty buckets, divide-by-zero) and encoding/json rejects those outright
// ("json: unsupported value: NaN"), which would otherwise fail the whole query
// response.
func (d DataPoint) MarshalJSON() ([]byte, error) {
	if math.IsNaN(d.Value) || math.IsInf(d.Value, 0) {
		return []byte(`{"timestamp":` + strconv.FormatInt(d.Timestamp, 10) + `,"value":null}`), nil
	}
	// plain strips the MarshalJSON method so the finite path keeps stdlib's
	// exact float formatting (no recursion).
	type plain DataPoint
	return json.Marshal(plain(d))
}

// promResponse is the raw shape returned by Prometheus HTTP API
// /api/v1/query and /api/v1/query_range endpoints.
type promResponse struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	ErrorType string          `json:"errorType,omitempty"`
	Error     string          `json:"error,omitempty"`
}
