package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	cnpgClusterLabel          = "cnpg.io/cluster"
	cnpgDefaultLogContainer   = "postgres"
	cnpgDefaultLogTailLines   = 200
	cnpgLogDiscoveryInterval  = 5 * time.Second
	cnpgLogsEmptyMessage      = "No readable logs from this cluster's instances in this snapshot. Refresh after the instances start."
	cnpgLogsNoInstanceMessage = "This cluster has no instance Pods yet."
)

// CNPGClusterLogsResponse is GET /api/cnpg/clusters/{namespace}/{name}/logs.
// Pods and SourceLabels list only the instances that contributed a source to
// this snapshot; SourceLabels maps a Pod to its role and ordinal ("replica 2").
type CNPGClusterLogsResponse struct {
	UID          types.UID          `json:"uid"`
	Pods         []WorkloadPodInfo  `json:"pods"`
	Logs         []workloadLogEntry `json:"logs"`
	Notice       string             `json:"notice"`
	SourceLabels map[string]string  `json:"sourceLabels,omitempty"`
	CapturedAt   string             `json:"capturedAt"`
	EmptyMessage string             `json:"emptyMessage"`
}

type cnpgLogQuery struct {
	container    string
	tailLines    int64
	sinceSeconds *int64
	sinceTime    time.Time
	untilTime    time.Time
	pod          string
}

func parseCNPGLogQuery(r *http.Request, now time.Time) (cnpgLogQuery, error) {
	q := r.URL.Query()
	out := cnpgLogQuery{
		container:    q.Get("container"),
		tailLines:    parseTailLines(q.Get("tailLines"), cnpgDefaultLogTailLines),
		sinceSeconds: parseSinceSeconds(q.Get("sinceSeconds")),
		pod:          q.Get("pod"),
	}
	if out.container == "" {
		out.container = cnpgDefaultLogContainer
	}
	if raw := q.Get("sinceTime"); raw != "" {
		if q.Get("sinceSeconds") != "" {
			return out, errors.New("sinceSeconds and sinceTime are mutually exclusive")
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return out, fmt.Errorf("invalid sinceTime %q (expected RFC3339)", raw)
		}
		out.sinceTime = t
		// The pod log API takes whole seconds; round up and trim the overlap
		// from the entries afterwards.
		secs := max(int64(math.Ceil(now.Sub(t).Seconds())), 1)
		out.sinceSeconds = &secs
	}
	if raw := q.Get("untilTime"); raw != "" {
		if out.sinceTime.IsZero() {
			return out, errors.New("untilTime requires sinceTime")
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return out, fmt.Errorf("invalid untilTime %q (expected RFC3339)", raw)
		}
		if !t.After(out.sinceTime) {
			return out, errors.New("untilTime must be after sinceTime")
		}
		out.untilTime = t
		// An interval is read from its start: the pod log API has no upper
		// bound, and a tail would return the lines nearest now instead.
		if q.Get("tailLines") == "" {
			out.tailLines = 0
		}
	}
	return out, nil
}

func (q cnpgLogQuery) keep(entry workloadLogEntry) bool {
	if q.sinceTime.IsZero() || entry.Timestamp == "" {
		return true
	}
	ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return true
	}
	return !ts.Before(q.sinceTime) && (q.untilTime.IsZero() || !ts.After(q.untilTime))
}

// authorizeCNPGClusterLogs gates on reading the Cluster, listing its Pods and
// reading their logs — before the Cluster is looked up, so a denied caller
// cannot probe which Clusters exist.
func (s *Server) authorizeCNPGClusterLogs(w http.ResponseWriter, r *http.Request, namespace string) bool {
	if !s.requireConnected(w) {
		return false
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return false
	}
	if !s.canRead(r, cnpgGroup, "clusters", namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to clusters.postgresql.cnpg.io in namespace "+namespace)
		return false
	}
	if !s.canRead(r, "", "pods", namespace, "list") {
		s.writeError(w, http.StatusForbidden, "no access to pods in namespace "+namespace)
		return false
	}
	return s.authorizePodLogRead(w, r, namespace)
}

// loadCNPGCluster reads one CNPG Cluster from the dynamic cache. The error is
// already written when ok is false.
func (s *Server) loadCNPGCluster(w http.ResponseWriter, r *http.Request, cache *k8s.ResourceCache, namespace, name string) (*unstructured.Unstructured, bool) {
	cluster, err := findCNPGCluster(r.Context(), cache, namespace, name)
	switch {
	case err == nil && cluster != nil:
		return cluster, true
	case err == nil, errors.Is(err, k8s.ErrUnknownDynamicKind):
		s.writeError(w, http.StatusNotFound, "CloudNativePG Cluster "+namespace+"/"+name+" not found")
	case errors.Is(err, errDynamicNotSynced):
		s.writeError(w, http.StatusServiceUnavailable, "CloudNativePG Clusters are still syncing")
	default:
		log.Printf("[cnpg] Failed to read Cluster %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to read CloudNativePG Cluster")
	}
	return nil, false
}

func findCNPGCluster(ctx context.Context, cache *k8s.ResourceCache, namespace, name string) (*unstructured.Unstructured, error) {
	clusters, err := filterCNPGGroup(listDynamicSynced(ctx, cache, "Cluster", cnpgGroup, namespace))
	if err != nil {
		return nil, err
	}
	for _, c := range clusters {
		if c.GetNamespace() == namespace && c.GetName() == name && c.GroupVersionKind().Group == cnpgGroup {
			return c, nil
		}
	}
	return nil, nil
}

// cnpgClusterInstancePods returns the Cluster's instance Pods under the same
// label-and-controller-UID rule the workspace uses, sorted by name.
func cnpgClusterInstancePods(cache *k8s.ResourceCache, cluster *unstructured.Unstructured) ([]*corev1.Pod, error) {
	lister := cache.Pods()
	if lister == nil {
		return nil, errors.New("pod cache unavailable")
	}
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	candidates, err := lister.Pods(namespace).List(labels.SelectorFromSet(labels.Set{cnpgClusterLabel: name}))
	if err != nil {
		return nil, err
	}
	uids := map[string]types.UID{namespace + "/" + name: cluster.GetUID()}
	pods := make([]*corev1.Pod, 0, len(candidates))
	for _, p := range candidates {
		if p != nil && isCNPGInstancePod(p, uids) {
			pods = append(pods, p)
		}
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods, nil
}

func cnpgInstanceRole(p *corev1.Pod) string {
	if role := p.Labels["cnpg.io/instanceRole"]; role != "" {
		return role
	}
	return p.Labels["role"]
}

// selectCNPGLogPods narrows to the requested instance. ok is false when the
// requested Pod is not one of the Cluster's instances.
func selectCNPGLogPods(pods []*corev1.Pod, want string) ([]*corev1.Pod, bool) {
	if want == "" {
		return pods, true
	}
	for _, p := range pods {
		if p.Name == want {
			return []*corev1.Pod{p}, true
		}
	}
	return nil, false
}

// cnpgLogRecord is the subset of a CloudNativePG instance-manager JSON log
// line the viewer surfaces. PostgreSQL's own log lines arrive wrapped, with the
// server's severity and message under record.
type cnpgLogRecord struct {
	Level  string `json:"level"`
	Logger string `json:"logger"`
	Msg    string `json:"msg"`
	Error  any    `json:"error"`
	Record *struct {
		ErrorSeverity string `json:"error_severity"`
		Message       string `json:"message"`
	} `json:"record"`
}

// annotateCNPGLogEntry fills the parsed fields of a CloudNativePG log line and
// leaves anything that is not one untouched.
func annotateCNPGLogEntry(entry *workloadLogEntry) {
	content := strings.TrimSpace(entry.Content)
	if !strings.HasPrefix(content, "{") {
		return
	}
	var rec cnpgLogRecord
	if err := json.Unmarshal([]byte(content), &rec); err != nil {
		return
	}
	level := strings.ToUpper(rec.Level)
	message := rec.Msg
	if rec.Record != nil {
		if rec.Record.ErrorSeverity != "" {
			level = rec.Record.ErrorSeverity
		}
		if rec.Record.Message != "" {
			message = rec.Record.Message
		}
	}
	if errText := cnpgLogErrorText(rec.Error); errText != "" {
		if message == "" {
			message = errText
		} else {
			message += ": " + errText
		}
	}
	entry.Level, entry.Logger, entry.Message = level, rec.Logger, message
}

func cnpgLogErrorText(v any) string {
	switch e := v.(type) {
	case nil:
		return ""
	case string:
		return e
	default:
		b, err := json.Marshal(e)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// handleCNPGClusterLogs serves GET /api/cnpg/clusters/{namespace}/{name}/logs:
// a bounded snapshot of every instance Pod's logs, merged by timestamp.
func (s *Server) handleCNPGClusterLogs(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGClusterLogs(w, r, namespace) {
		return
	}
	query, qerr := parseCNPGLogQuery(r, time.Now())
	if qerr != nil {
		s.writeError(w, http.StatusBadRequest, qerr.Error())
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	cluster, ok := s.loadCNPGCluster(w, r, cache, namespace, name)
	if !ok {
		return
	}
	instances, err := cnpgClusterInstancePods(cache, cluster)
	if err != nil {
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusServiceUnavailable, "instance Pods unavailable: "+err.Error())
		return
	}
	pods, ok := selectCNPGLogPods(instances, query.pod)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "pod "+query.pod+" is not an instance of CloudNativePG Cluster "+namespace+"/"+name)
		return
	}

	resp := CNPGClusterLogsResponse{
		UID:          cluster.GetUID(),
		Pods:         []WorkloadPodInfo{},
		Logs:         []workloadLogEntry{},
		CapturedAt:   time.Now().UTC().Format(time.RFC3339),
		EmptyMessage: cnpgLogsEmptyMessage,
	}
	if len(pods) == 0 {
		resp.EmptyMessage = cnpgLogsNoInstanceMessage
		s.writeJSON(w, resp)
		return
	}
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client unavailable")
		return
	}

	snapshot := collectLogsFromPods(r.Context(), client, namespace, pods, query.container, query.tailLines, query.sinceSeconds, true)
	shown := []*corev1.Pod{}
	sourceLabels := map[string]string{}
	for _, p := range pods {
		if !snapshot.SourcePods[p.Name] {
			continue
		}
		shown = append(shown, p)
		if role := cnpgInstanceRole(p); role != "" {
			sourceLabels[p.Name] = cnpgInstanceSourceLabel(p, role)
		}
	}
	for _, entry := range snapshot.Logs {
		if !query.keep(entry) {
			continue
		}
		entry.SourceLabel = sourceLabels[entry.Pod]
		annotateCNPGLogEntry(&entry)
		resp.Logs = append(resp.Logs, entry)
	}
	sortLogsByTimestamp(resp.Logs)
	resp.Pods = buildPodInfos(shown)
	resp.Notice = snapshot.Notice
	if len(sourceLabels) > 0 {
		resp.SourceLabels = sourceLabels
	}
	s.writeJSON(w, resp)
}

// handleCNPGClusterLogsStream serves GET
// /api/cnpg/clusters/{namespace}/{name}/logs/stream: an SSE follow of every
// instance Pod, re-resolving instances as the Cluster fails over or scales.
// Events: connected {cluster, namespace, uid, pods}, log (a log entry with
// the parsed fields), pod_added {pods}, pod_removed {pod, reason}, end
// {reason}, error {error}.
func (s *Server) handleCNPGClusterLogsStream(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGClusterLogs(w, r, namespace) {
		return
	}
	query, qerr := parseCNPGLogQuery(r, time.Now())
	if qerr != nil {
		s.writeError(w, http.StatusBadRequest, qerr.Error())
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	cluster, ok := s.loadCNPGCluster(w, r, cache, namespace, name)
	if !ok {
		return
	}
	instances, err := cnpgClusterInstancePods(cache, cluster)
	if err != nil {
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusServiceUnavailable, "instance Pods unavailable: "+err.Error())
		return
	}
	pods, ok := selectCNPGLogPods(instances, query.pod)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "pod "+query.pod+" is not an instance of CloudNativePG Cluster "+namespace+"/"+name)
		return
	}
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Printf("[cnpg] Failed to stream logs for %s/%s: response writer does not support flushing", namespace, name)
		s.writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	uid := cluster.GetUID()
	sendSSEEvent(w, flusher, "connected", map[string]any{
		"cluster": name, "namespace": namespace, "uid": uid, "pods": buildPodInfos(pods),
	})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	logCh := make(chan workloadLogEntry, 1000)
	var active sync.Map
	roles := map[string]string{}
	cursors := map[string]*cnpgStreamCursor{}
	start := func(pods []*corev1.Pod) {
		for _, pod := range pods {
			if role := cnpgInstanceRole(pod); role != "" {
				roles[pod.Name] = cnpgInstanceSourceLabel(pod, role)
			}
			for _, c := range k8s.GetContainersForPod(pod, query.container, true) {
				key := pod.Name + "/" + c
				if _, exists := active.Load(key); exists {
					continue
				}
				cursor := cursors[key]
				if cursor == nil {
					cursor = &cnpgStreamCursor{}
					cursors[key] = cursor
				}
				opts := cursor.restartOptions(c, query.tailLines, query.sinceSeconds)
				streamCtx, streamCancel := context.WithCancel(ctx)
				handle := &cnpgStreamHandle{cancel: streamCancel}
				active.Store(key, handle)
				go func(podName, key string) {
					defer active.CompareAndDelete(key, handle)
					followCNPGContainerLogs(streamCtx, client, namespace, podName, opts, logCh)
				}(pod.Name, key)
			}
		}
	}
	start(pods)

	known := map[string]bool{}
	for _, p := range pods {
		known[p.Name] = true
	}
	ticker := time.NewTicker(cnpgLogDiscoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-logCh:
			if cursor := cursors[entry.Pod+"/"+entry.Container]; cursor != nil && !cursor.admit(entry) {
				continue
			}
			if !query.keep(entry) {
				continue
			}
			entry.SourceLabel = roles[entry.Pod]
			annotateCNPGLogEntry(&entry)
			sendSSEEvent(w, flusher, "log", entry)
		case <-ticker.C:
			current, err := findCNPGCluster(ctx, cache, namespace, name)
			if err != nil {
				continue
			}
			if current == nil || current.GetUID() != uid {
				sendSSEEvent(w, flusher, "end", map[string]string{"reason": "cluster deleted"})
				return
			}
			all, err := cnpgClusterInstancePods(cache, current)
			if err != nil {
				continue
			}
			currentPods, _ := selectCNPGLogPods(all, query.pod)
			present := map[string]bool{}
			for _, p := range currentPods {
				present[p.Name] = true
				if !known[p.Name] {
					known[p.Name] = true
					sendSSEEvent(w, flusher, "pod_added", map[string]any{"pods": []WorkloadPodInfo{buildPodInfo(p, time.Now())}})
				}
			}
			for podName := range known {
				if present[podName] {
					continue
				}
				delete(known, podName)
				active.Range(func(key, value any) bool {
					if strings.HasPrefix(key.(string), podName+"/") {
						value.(*cnpgStreamHandle).cancel()
						active.Delete(key)
					}
					return true
				})
				for key := range cursors {
					if strings.HasPrefix(key, podName+"/") {
						delete(cursors, key)
					}
				}
				sendSSEEvent(w, flusher, "pod_removed", map[string]string{"pod": podName, "reason": "terminated"})
			}
			start(currentPods)
		}
	}
}

type cnpgStreamHandle struct {
	cancel context.CancelFunc
}

// cnpgStreamCursor remembers where one container's follow left off, so a
// stream that ends while its Pod is still an instance resumes instead of
// replaying lines the client already has. Only the stream loop touches it.
type cnpgStreamCursor struct {
	last time.Time
	// atLast holds the contents delivered with timestamp == last. The pod log
	// API's sinceTime is second-granular, so a resume replays that second and
	// only (timestamp, content) tells a replay from a new line.
	atLast map[string]bool
}

// restartOptions returns the follow request for the next (re)start: the
// caller's window the first time, and from the last delivered second after.
func (c *cnpgStreamCursor) restartOptions(container string, tailLines int64, sinceSeconds *int64) corev1.PodLogOptions {
	opts := corev1.PodLogOptions{Container: container, Timestamps: true, Follow: true}
	if c.last.IsZero() {
		opts.TailLines = &tailLines
		opts.SinceSeconds = sinceSeconds
		return opts
	}
	since := metav1.NewTime(c.last.Truncate(time.Second))
	opts.SinceTime = &since
	return opts
}

// admit reports whether an entry is new, recording it when it is. Lines
// arrive in order per container, so anything before the last delivered
// timestamp was already sent.
func (c *cnpgStreamCursor) admit(entry workloadLogEntry) bool {
	ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return true
	}
	switch {
	case ts.Before(c.last):
		return false
	case ts.Equal(c.last):
		if c.atLast[entry.Content] {
			return false
		}
	default:
		c.last = ts
		c.atLast = map[string]bool{}
	}
	c.atLast[entry.Content] = true
	return true
}

func followCNPGContainerLogs(ctx context.Context, client kubernetes.Interface, namespace, podName string, opts corev1.PodLogOptions, logCh chan<- workloadLogEntry) {
	stream, err := client.CoreV1().Pods(namespace).GetLogs(podName, &opts).Stream(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[cnpg] Failed to follow logs for %s/%s/%s: %v", namespace, podName, opts.Container, err)
		}
		return
	}
	defer stream.Close()
	reader := bufio.NewReader(stream)
	for {
		line, err := reader.ReadString('\n')
		if line = strings.TrimSuffix(line, "\n"); line != "" && (err == nil || err == io.EOF) {
			ts, content := parseLogLine(line)
			select {
			case logCh <- workloadLogEntry{Pod: podName, Container: opts.Container, Timestamp: ts, Content: content}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				log.Printf("[cnpg] Failed to read logs for %s/%s/%s: %v", namespace, podName, opts.Container, err)
			}
			return
		}
	}
}

// cnpgInstanceSourceLabel names an instance by role and ordinal ("replica 3"):
// the role alone cannot tell two replicas apart.
func cnpgInstanceSourceLabel(p *corev1.Pod, role string) string {
	name := p.Labels["cnpg.io/instanceName"]
	if name == "" {
		name = p.Name
	}
	if i := strings.LastIndex(name, "-"); i >= 0 && i < len(name)-1 {
		return role + " " + name[i+1:]
	}
	return role
}
