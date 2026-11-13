package server

import (
	"context"
	"net/http"

	"github.com/skyhook-io/radar/internal/loki"
)

// handleLokiStatus reports the Loki connection: whether it is configured and
// reachable, its redacted address and the stream labels Radar detected. Like
// the metrics status it is not gated beyond the shared same-origin check: it
// names no cluster object and never carries a header value or a full URL.
// When Loki is configured but not connected, it starts a connection attempt
// in the background (discovery may open a port-forward with Radar's
// identity, as the metrics status does); the response is always the cached
// state. The address names the discovered Service, like the metrics status;
// on a shared listener without auth that is visible to anyone who can reach
// Radar, and a cross-site page is refused by requireSameOrigin.
func (s *Server) handleLokiStatus(w http.ResponseWriter, r *http.Request) {
	st := loki.CurrentStatus()
	if st.Configured && !st.Connected && !st.Discovering {
		go func() { _ = loki.EnsureConnected(context.Background()) }()
	}
	s.writeJSON(w, st)
}
