package server

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
)

// connectionHeader carries the connection a browser tab is showing (on
// requests) and the connection that produced a response (on responses).
const connectionHeader = "X-Radar-Connection"

// noConnection stamps a response no admitted connection produced end to end:
// nothing was serving, or the serving connection changed while it was built.
const noConnection = "none"

const (
	errCodeContextChanged  = "context_changed"
	errCodeClusterChanging = "cluster_changing"
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
		label := connectionIdentityPublished()
		// Refusals are labelled too, so the caller knows to catch up.
		bound, err := k8s.CaptureBoundConnection()
		if err != nil {
			if label {
				w.Header().Set(connectionHeader, noConnection)
			}
			writeBindingError(w, http.StatusServiceUnavailable, errCodeClusterChanging,
				"Nothing was changed: Radar is switching clusters. Try again when it finishes.")
			return
		}
		if label {
			w.Header().Set(connectionHeader, bound.ID)
		}
		if want := r.Header.Get(connectionHeader); label && want != "" && want != bound.ID {
			writeBindingError(w, http.StatusConflict, errCodeContextChanged,
				"Nothing was changed: the cluster changed since this view loaded. Check the current cluster and try again.")
			return
		}
		next(w, r.WithContext(k8s.WithBoundConnection(r.Context(), bound)))
	}
}

// stampRead labels a read with the connection that served it, so a browser
// showing another connection discards it rather than mixing clusters. The
// label is decided when the headers go out: if the serving connection changed
// while the handler read its caches, the data can't be attributed to either,
// and the response says so.
func (s *Server) stampRead(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !connectionIdentityPublished() {
			next(w, r)
			return
		}
		next(&connectionStampWriter{ResponseWriter: w, admitted: k8s.ServingConnectionID()}, r)
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
		status.ConnectionID, status.ConnectionSeq, status.BootAt = "", 0, ""
	}
	return status
}

// connectionIdentityFields is the identity part of a published status.
func connectionIdentityFields(status k8s.ConnectionStatus, into map[string]any) map[string]any {
	if connectionIdentityPublished() {
		into["connectionId"] = status.ConnectionID
		into["seq"] = status.ConnectionSeq
		into["bootAt"] = status.BootAt
	}
	return into
}

type connectionStampWriter struct {
	http.ResponseWriter
	admitted string
	stamped  bool
}

func (cw *connectionStampWriter) stamp() {
	if cw.stamped {
		return
	}
	cw.stamped = true
	id := cw.admitted
	if id == "" || k8s.ServingConnectionID() != id {
		id = noConnection
	}
	cw.Header().Set(connectionHeader, id)
}

func (cw *connectionStampWriter) WriteHeader(status int) {
	cw.stamp()
	cw.ResponseWriter.WriteHeader(status)
}

func (cw *connectionStampWriter) Write(b []byte) (int, error) {
	cw.stamp()
	return cw.ResponseWriter.Write(b)
}

func (cw *connectionStampWriter) Unwrap() http.ResponseWriter { return cw.ResponseWriter }

func writeBindingError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Both code fields: the action helpers read `code`, generic errors `error_code`.
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message, "code": code, "error_code": code}); err != nil {
		log.Printf("Failed to encode error response: %v", err)
	}
}
