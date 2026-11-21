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
		bound, err := k8s.CaptureBoundConnection()
		if err != nil {
			writeBindingError(w, http.StatusServiceUnavailable, errCodeClusterChanging,
				"Radar is switching clusters. Try again when it finishes.")
			return
		}
		if want := r.Header.Get(connectionHeader); want != "" && want != bound.ID {
			writeBindingError(w, http.StatusConflict, errCodeContextChanged,
				"The cluster changed since this view loaded. Reload the view and try again.")
			return
		}
		w.Header().Set(connectionHeader, bound.ID)
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
		next(&connectionStampWriter{ResponseWriter: w, admitted: k8s.ServingConnectionID()}, r)
	}
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
