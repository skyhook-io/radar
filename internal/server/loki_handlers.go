package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/loki"
	"github.com/skyhook-io/radar/internal/readscope"
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

type lokiWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type lokiPodLogsResponse struct {
	LogsResponse
	Source    string     `json:"source"` // "loki"
	Truncated bool       `json:"truncated"`
	Limit     int        `json:"limit"`
	Window    lokiWindow `json:"window"`
}

// noContainerLabel keys the lines when Loki's streams carry no container.
const noContainerLabel = "(all containers)"

// handleLokiPodLogs reads one pod's log history from Loki: the pod may be
// gone, so it is not looked up. Gate: namespace access plus get pods/log, the
// same as live logs, checked before Loki is contacted. The query is built
// only from the namespace, pod, container and window (fixed LogQL, values
// escaped); no other parameter reaches Loki. Through Radar Hub it is a GET.
// On a shared listener without auth it reads as Radar's identity, which can
// already read the same pod live; a cross-site request is refused by
// requireSameOrigin.
func (s *Server) handleLokiPodLogs(w http.ResponseWriter, r *http.Request) {
	namespace, podName := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if allowed := s.getUserNamespaces(r, []string{namespace}); readscope.NoNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	if !s.authorizePodLogRead(w, r, namespace) {
		return
	}
	q := r.URL.Query()
	window, err := loki.ParseWindow(q, time.Now())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := lokiLimit(q.Get("limit"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	labels, err := s.lokiLabels(r)
	if err != nil {
		s.writeLokiError(w, err)
		return
	}
	container := q.Get("container")
	selector, err := loki.PodSelector(labels, namespace, podName, container)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	lines, err := loki.QueryRange(r.Context(), selector, window, limit)
	if err != nil {
		s.writeLokiError(w, err)
		return
	}
	resp := lokiPodLogsResponse{
		LogsResponse: LogsResponse{PodName: podName, Namespace: namespace, Containers: []string{}, Logs: map[string]string{}},
		Source:       "loki", Truncated: lines.Truncated, Limit: limit, Window: lokiWindow{window.Start, window.End},
	}
	var b = map[string]*strings.Builder{}
	for _, l := range lines.Items {
		c := l.Container
		if c == "" {
			c = noContainerLabel
		}
		if b[c] == nil {
			b[c] = &strings.Builder{}
			resp.Containers = append(resp.Containers, c)
		}
		b[c].WriteString(l.Time.Format(time.RFC3339Nano) + " " + l.Text + "\n")
	}
	sort.Strings(resp.Containers)
	for c, sb := range b {
		resp.Logs[c] = sb.String()
	}
	s.writeJSON(w, resp)
}

func lokiLimit(raw string) (int, error) {
	if raw == "" {
		return loki.DefaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errors.New("limit must be a positive number")
	}
	return min(n, loki.DefaultLimit), nil
}

// lokiLabels connects if needed and returns the detected stream labels.
func (s *Server) lokiLabels(r *http.Request) (loki.StreamLabels, error) {
	if labels, ok := loki.Labels(); ok {
		return labels, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := loki.EnsureConnected(ctx); err != nil {
		return loki.StreamLabels{}, err
	}
	labels, _ := loki.Labels()
	return labels, nil
}

// writeLokiError: 502 when Loki answered with an error (its message,
// redacted), 503 when Loki is off or unreachable.
func (s *Server) writeLokiError(w http.ResponseWriter, err error) {
	var upstream *loki.UpstreamError
	switch {
	case errors.As(err, &upstream):
		s.writeError(w, http.StatusBadGateway, "Loki answered: "+upstream.Message)
	case errors.Is(err, loki.ErrNotConfigured):
		s.writeError(w, http.StatusServiceUnavailable, "Loki is not connected")
	default:
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
	}
}
