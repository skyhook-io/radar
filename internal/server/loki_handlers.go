package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

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

// lokiTimeLayout is fixed-width (RFC3339Nano drops trailing zeros), so the
// text order of lines is their time order and the log parser always finds the
// time.
const lokiTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// radarCanReadPodLogs asks whether Radar's own identity may get pods/log in
// namespace (a SelfSubjectAccessReview).
var radarCanReadPodLogs = func(ctx context.Context, namespace string) bool {
	allowed, _ := k8score.CanI(ctx, k8s.GetClientInterface(), namespace, "", "pods/log", "get")
	return allowed
}

// noContainerLabel keys the lines when Loki's streams carry no container.
const noContainerLabel = "(all containers)"

// handleLokiPodLogs reads one pod's log history from Loki: the pod may be
// gone, so it is not looked up. Gate: namespace access plus get pods/log, the
// same as live logs, checked before Loki is contacted. The query is built
// only from the namespace, pod, container and window (fixed LogQL, values
// escaped); no other parameter reaches Loki. Through Radar Hub it is a GET.
// --namespace-scope applies. Without authentication there is no caller to
// check, and Loki never asks Kubernetes, so Radar's own identity must be able
// to get pods/log in the namespace: History never reads more than live logs
// could. A cross-site request is refused by requireSameOrigin.
func (s *Server) handleLokiPodLogs(w http.ResponseWriter, r *http.Request) {
	namespace, podName := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if allowed := s.getUserNamespaces(r, []string{namespace}); readscope.NoNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	if !s.authorizePodLogRead(w, r, namespace) || !s.lokiWithinRadar(w, r, namespace) {
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
	lines, err := loki.QueryRange(r.Context(), labels, selector, window, limit)
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
		b[c].WriteString(l.Time.Format(lokiTimeLayout) + " " + l.Text + "\n")
	}
	sort.Strings(resp.Containers)
	for c, sb := range b {
		resp.Logs[c] = sb.String()
	}
	s.writeJSON(w, resp)
}

// lokiWithinRadar keeps a Loki read within what Radar itself may read:
// --namespace-scope, and, without authentication (no caller was checked, and
// Loki never asks Kubernetes), Radar's own get pods/log in the namespace.
func (s *Server) lokiWithinRadar(w http.ResponseWriter, r *http.Request, namespace string) bool {
	if k8s.ForceNamespaceScope && namespace != k8s.GetNamespaceScopeTarget() {
		s.writeError(w, http.StatusForbidden, "namespace "+namespace+" is outside this Radar's namespace scope")
		return false
	}
	if auth.UserFromContext(r.Context()) == nil && !radarCanReadPodLogs(r.Context(), namespace) {
		s.writeError(w, http.StatusForbidden, "Radar's identity can't read pod logs in namespace "+namespace+" (get pods/log)")
		return false
	}
	return true
}

type lokiJobPod struct {
	Name       string   `json:"name"`
	Containers []string `json:"containers"`
	Ready      bool     `json:"ready"`
}

type lokiJobLine struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
}

// lokiJobLogsResponse has the workload logs shape, so the workload logs
// viewer shows it as it shows a live run.
type lokiJobLogsResponse struct {
	Pods      []lokiJobPod  `json:"pods"`
	Logs      []lokiJobLine `json:"logs"`
	Notice    string        `json:"notice,omitempty"`
	Source    string        `json:"source"` // "loki"
	Truncated bool          `json:"truncated"`
	Limit     int           `json:"limit"`
	Window    lokiWindow    `json:"window"`
}

// jobRunSlack widens a run's window on each side for clock skew and shipping delay.
const jobRunSlack = time.Minute

// jobRunWindow is the run's own time: its start to its completion, last
// failure or now, plus slack, capped to the newest MaxWindow.
func jobRunWindow(job *batchv1.Job, now time.Time) (loki.Window, bool) {
	if job.Status.StartTime == nil {
		return loki.Window{}, false
	}
	end := now
	if job.Status.CompletionTime != nil {
		end = job.Status.CompletionTime.Time
	} else {
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
				end = c.LastTransitionTime.Time
			}
		}
	}
	w := loki.Window{Start: job.Status.StartTime.Add(-jobRunSlack), End: end.Add(jobRunSlack)}
	if w.End.Sub(w.Start) > loki.MaxWindow {
		w.Start = w.End.Add(-loki.MaxWindow)
	}
	return w, true
}

// handleLokiJobLogs reads one Job run's logs from Loki when its pods are gone.
// Only a Job Kubernetes still holds: its start and finish bound the window
// (start/end override it, within 7 days). Gate: the workload logs gate
// (namespace, get jobs, list pods, get pods/log) plus lokiWithinRadar, all
// before Loki is contacted. The query selects the Job's pods by a Job stream
// label when Loki has one, else by the Job controller's pod names; only the
// namespace, Job name and window reach it.
func (s *Server) handleLokiJobLogs(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeWorkloadLogRead(w, r, "jobs", namespace) || !s.lokiWithinRadar(w, r, namespace) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Jobs() == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	job, err := cache.Jobs().Jobs(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		s.writeError(w, http.StatusNotFound, "job "+namespace+"/"+name+" not found")
		return
	}
	if err != nil {
		log.Printf("[loki] Failed to get job %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	q := r.URL.Query()
	var window loki.Window
	if q.Get("start") != "" || q.Get("end") != "" {
		if window, err = loki.ParseWindow(q, time.Now()); err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else if w2, ok := jobRunWindow(job, time.Now()); ok {
		window = w2
	} else {
		s.writeError(w, http.StatusConflict, "job "+namespace+"/"+name+" has not started")
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
	indexed := job.Spec.CompletionMode != nil && *job.Spec.CompletionMode == batchv1.IndexedCompletion
	selector, notice, err := loki.JobSelector(labels, namespace, name, indexed)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	lines, err := loki.QueryRange(r.Context(), labels, selector, window, limit)
	if err != nil {
		s.writeLokiError(w, err)
		return
	}
	resp := lokiJobLogsResponse{Pods: []lokiJobPod{}, Logs: []lokiJobLine{}, Notice: notice, Source: "loki", Truncated: lines.Truncated, Limit: limit, Window: lokiWindow{window.Start, window.End}}
	containers := map[string]map[string]bool{}
	for _, l := range lines.Items {
		c := l.Container
		if c == "" {
			c = noContainerLabel
		}
		if containers[l.Pod] == nil {
			containers[l.Pod] = map[string]bool{}
		}
		containers[l.Pod][c] = true
		resp.Logs = append(resp.Logs, lokiJobLine{Pod: l.Pod, Container: c, Timestamp: l.Time.Format(lokiTimeLayout), Content: l.Text})
	}
	for pod, cs := range containers {
		p := lokiJobPod{Name: pod, Containers: []string{}}
		for c := range cs {
			p.Containers = append(p.Containers, c)
		}
		sort.Strings(p.Containers)
		resp.Pods = append(resp.Pods, p)
	}
	sort.Slice(resp.Pods, func(i, j int) bool { return resp.Pods[i].Name < resp.Pods[j].Name })
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
