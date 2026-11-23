package server

import (
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
)

// connectionHeader carries the connection a browser tab is on; connectionParam
// carries it on a WebSocket URL.
const (
	connectionHeader = "X-Radar-Connection"
	connectionParam  = "connection"
)

const (
	errCodeContextChanged     = "context_changed"
	errCodeClusterUnavailable = "cluster_unavailable"
)

// bindWrite admits a write against the connection the caller is showing.
//
// It captures the serving connection's ID, clients and discovery in one step
// and runs the handler on that capture, so a switch that lands mid-request
// can't redirect the write. A request that names a connection other than the
// one serving is refused before any Kubernetes work; one that names none is
// admitted against the current connection (API and MCP callers, older UIs).
func (s *Server) bindWrite(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bound, ok := s.admitWrite(w, r)
		if !ok {
			return
		}
		next(w, r.WithContext(k8s.WithConnection(r.Context(), bound)))
	}
}

// admitWrite is bindWrite's check: it captures the serving connection, or
// answers the refusal and returns false. For handlers that only learn from
// the body whether a request writes to the cluster.
func (s *Server) admitWrite(w http.ResponseWriter, r *http.Request) (*k8s.Connection, bool) {
	bound, err := k8s.CaptureServingConnection()
	if err != nil {
		// Nothing serving: a switch, rescope or rebuild in progress, a lost
		// connection, or shutdown. The caller can retry once one is serving.
		s.writeErrorCode(w, http.StatusServiceUnavailable, errCodeClusterUnavailable,
			"Nothing was changed: Radar has no usable cluster connection right now. Try again once it is connected.")
		return nil, false
	}
	want := r.Header.Get(connectionHeader)
	if want == "" {
		// A browser can't set headers on a WebSocket; it names the connection in the URL.
		want = r.URL.Query().Get(connectionParam)
	}
	if connectionIdentityPublished() && want != "" && want != bound.ID() {
		s.writeErrorCode(w, http.StatusConflict, errCodeContextChanged,
			"Nothing was changed: the cluster changed since this view loaded. Check the current cluster and try again.")
		return nil, false
	}
	return bound, true
}

// execWrite is bindWrite for routes that --disable-exec turns off: that
// refusal names the setting, so it comes before the connection check.
func (s *Server) execWrite(next http.HandlerFunc) http.HandlerFunc {
	bound := s.bindWrite(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireExecEnabled(w) {
			return
		}
		bound(w, r)
	}
}

// connectionIdentityPublished reports whether browsers see connection IDs.
// Only a Radar serving from a kubeconfig can change clusters; an in-cluster
// Radar serves one cluster for its whole life, and during a rolling update two
// of its pods (each with its own IDs) answer the same browser, so it publishes
// none and its browsers bind nothing.
func connectionIdentityPublished() bool {
	return !k8s.IsInCluster()
}

// publishedStatus is a status as browsers may see it: without identity where
// none is published.
func publishedStatus(status k8s.ConnectionStatus) k8s.ConnectionStatus {
	if !connectionIdentityPublished() {
		status.ConnectionID = ""
	}
	return status
}

// connectionIdentityFields adds the connection ID to a status map where it is published.
func connectionIdentityFields(status k8s.ConnectionStatus, into map[string]any) map[string]any {
	if connectionIdentityPublished() {
		into["connectionId"] = status.ConnectionID
	}
	return into
}
