package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/loki"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// handleLokiStatus reports the Loki connection: whether it is configured and
// reachable, its redacted address and the stream labels Radar detected. Like
// the metrics status it is not gated beyond the shared same-origin check: it
// names no cluster object and never carries a header value or a full URL.
// When Loki is configured but not connected, it starts a connection attempt
// in the background (discovery may open a port-forward with Radar's
// identity), at most once per cooldown after a failure, so polling never
// becomes repeated discovery; the response is always the cached state. The address names the discovered Service, like the metrics status;
// on a shared listener without auth that is visible to anyone who can reach
// Radar, and a cross-site page is refused by requireSameOrigin.
func (s *Server) handleLokiStatus(w http.ResponseWriter, r *http.Request) {
	loki.Kick()
	s.writeJSON(w, loki.CurrentStatus())
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

// lokiTimeLayout is fixed-width (RFC3339Nano drops trailing zeros), so the
// text order of lines is their time order and the log parser always finds the
// time.
const lokiTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// radarCanReadPodLogs asks whether Radar's own identity may get pods/log in
// namespace (a SelfSubjectAccessReview), remembered per context and namespace
// for a short while so each read doesn't cost a review.
var radarCanReadPodLogs = func(ctx context.Context, namespace string) bool {
	return radarPodLogAccess(ctx, k8s.GetContextName()+"\x00"+namespace)
}

var radarPodLogAccess = memoizeAccess(30*time.Second, time.Now, func(ctx context.Context, key string) (bool, error) {
	_, namespace, _ := strings.Cut(key, "\x00")
	allowed, apiErr := k8score.CanI(ctx, k8s.GetClientInterface(), namespace, "", "pods/log", "get")
	if apiErr {
		return false, errors.New("access review failed")
	}
	return allowed, nil
})

// memoizeAccessMax bounds the memo: the namespace comes from the URL.
const memoizeAccessMax = 1024

// memoizeAccess remembers check's answers for ttl. A failed check is not
// remembered.
func memoizeAccess(ttl time.Duration, now func() time.Time, check func(context.Context, string) (bool, error)) func(context.Context, string) bool {
	type entry struct {
		allowed bool
		at      time.Time
	}
	var mu sync.Mutex
	seen := map[string]entry{}
	return func(ctx context.Context, key string) bool {
		mu.Lock()
		e, ok := seen[key]
		mu.Unlock()
		if ok && now().Sub(e.at) < ttl {
			return e.allowed
		}
		allowed, err := check(ctx, key)
		if err != nil {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) >= memoizeAccessMax {
			clear(seen)
		}
		seen[key] = entry{allowed, now()}
		return allowed
	}
}

// noContainerLabel keys the lines when Loki's streams carry no container.
const noContainerLabel = "(all containers)"

// handleLokiPodLogs reads one pod's log history from Loki: the pod may be
// gone, so it is not looked up. Gate: namespace access plus get pods/log, the
// same as live logs, checked before Loki is contacted. The query is built
// only from the namespace, pod, container, window and the --loki-cluster
// value (fixed LogQL, values
// escaped); no other parameter reaches Loki. Through Radar Hub it is a GET.
// --namespace-scope applies. Without authentication there is no caller to
// check, and Loki never asks Kubernetes, so Radar's own identity must be able
// to get pods/log in the namespace. Loki is matched by namespace and pod
// name only, not by this cluster: when one Loki holds several clusters' logs
// the read adds a cluster matcher only from --loki-cluster (and with
// authentication refuses while Loki has a cluster label and no value is
// set); a cluster label Radar doesn't detect and nobody named is not
// matched, and the same namespace and pod name in another cluster then
// reads too. A cross-site request is refused by requireSameOrigin.
func (s *Server) handleLokiPodLogs(w http.ResponseWriter, r *http.Request) {
	namespace, podName := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if allowed := s.getUserNamespaces(r, []string{namespace}); readscope.NoNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	if k8s.ForceNamespaceScope && namespace != k8s.GetNamespaceScopeTarget() {
		s.writeError(w, http.StatusForbidden, "namespace "+namespace+" is outside this Radar's namespace scope")
		return
	}
	if !s.authorizePodLogRead(w, r, namespace) {
		return
	}
	if auth.UserFromContext(r.Context()) == nil && !radarCanReadPodLogs(r.Context(), namespace) {
		s.writeError(w, http.StatusForbidden, "Radar's identity can't read pod logs in namespace "+namespace+" (get pods/log)")
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
	if reason := labels.HistoryReason(); reason != "" {
		s.writeError(w, http.StatusServiceUnavailable, reason)
		return
	}
	lines, err := loki.ReadPod(r.Context(), labels, namespace, podName, q.Get("container"), window, limit)
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
		// A multi-line entry (a stack trace) is one Loki entry: each of its
		// lines carries the entry's time, so the viewer keeps them in place.
		ts := l.Time.Format(lokiTimeLayout) + " "
		for _, text := range strings.Split(strings.TrimRight(l.Text, "\n"), "\n") {
			b[c].WriteString(ts + text + "\n")
		}
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

// writeLokiError: 429 when too many reads are running, 502 when Loki
// answered with an error (its message, redacted), 503 when Loki is off or
// unreachable.
func (s *Server) writeLokiError(w http.ResponseWriter, err error) {
	var upstream *loki.UpstreamError
	switch {
	case errors.Is(err, loki.ErrBusy):
		s.writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.As(err, &upstream):
		s.writeError(w, http.StatusBadGateway, "Loki answered: "+upstream.Message)
	case errors.Is(err, loki.ErrNotConfigured):
		s.writeError(w, http.StatusServiceUnavailable, "Loki is not connected")
	default:
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
	}
}
