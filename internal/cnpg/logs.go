package cnpg

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
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/podlogs"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// ClusterLogsResponse is GET /api/cnpg/clusters/{namespace}/{name}/logs.
// Pods and SourceLabels list only the instances that contributed a source to
// this snapshot; SourceLabels maps a Pod to its role and ordinal ("replica 2").
type ClusterLogsResponse struct {
	UID          types.UID         `json:"uid"`
	Pods         []podlogs.PodInfo `json:"pods"`
	Logs         []podlogs.Entry   `json:"logs"`
	Notice       string            `json:"notice"`
	SourceLabels map[string]string `json:"sourceLabels,omitempty"`
	CapturedAt   string            `json:"capturedAt"`
	EmptyMessage string            `json:"emptyMessage"`
}

// annotateCNPGLogEntry fills the parsed fields of a CloudNativePG log line and
// leaves anything that is not one untouched.
func annotateCNPGLogEntry(entry *podlogs.Entry) {
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

func cnpgContainerStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses, pod.Status.EphemeralContainerStatuses} {
		for i := range statuses {
			if statuses[i].Name == name {
				return &statuses[i]
			}
		}
	}
	return nil
}

// cnpgInstanceSourceLabel names an instance by role and ordinal ("replica 3"):
// the role alone cannot tell two replicas apart.
func cnpgInstanceSourceLabel(p *corev1.Pod, role string) string {
	name := p.Labels[instanceNameLabel]
	if name == "" {
		name = p.Name
	}
	if i := strings.LastIndex(name, "-"); i >= 0 && i < len(name)-1 {
		return role + " " + name[i+1:]
	}
	return role
}

// cnpgIntervalLogSources picks, per instance container, the runs whose lines
// can fall in [since, until]. The kubelet keeps a container's current run and
// the one before its last restart; an interval that ends before the current
// run started lives only in the previous run. lost counts containers where an
// older run than those two covered part of the interval.
func cnpgIntervalLogSources(pods []*corev1.Pod, container string, since, until time.Time) (sources []podlogs.Source, lost int) {
	for _, pod := range pods {
		for _, c := range cnpgLogContainers(pod, container) {
			status := cnpgContainerStatus(pod, c)
			if status == nil {
				continue
			}
			var currentStart time.Time
			switch {
			case status.State.Running != nil:
				currentStart = status.State.Running.StartedAt.Time
			case status.State.Terminated != nil:
				currentStart = status.State.Terminated.StartedAt.Time
			}
			started := status.State.Running != nil || status.State.Terminated != nil
			if started && (currentStart.IsZero() || !until.Before(currentStart)) {
				sources = append(sources, podlogs.NewSource(pod, c, false))
			}
			if !currentStart.IsZero() && !since.Before(currentStart) {
				continue
			}
			prev := status.LastTerminationState.Terminated
			if prev == nil {
				if status.RestartCount > 0 {
					lost++
				}
				continue
			}
			if prev.FinishedAt.IsZero() || !prev.FinishedAt.Time.Before(since) {
				sources = append(sources, podlogs.NewSource(pod, c, true))
			}
			if status.RestartCount > 1 && !prev.StartedAt.IsZero() && since.Before(prev.StartedAt.Time) {
				lost++
			}
		}
	}
	return sources, lost
}

func cnpgLogContainers(pod *corev1.Pod, selected string) []string {
	if selected != "all" {
		containers := k8s.GetContainersForPod(pod, selected, true)
		if len(containers) > 0 {
			return containers
		}
		for _, c := range pod.Spec.EphemeralContainers {
			if c.Name == selected {
				return []string{selected}
			}
		}
		return nil
	}
	containers := k8s.GetContainersForPod(pod, "", true)
	for _, c := range pod.Spec.InitContainers {
		containers = append(containers, c.Name)
	}
	for _, c := range pod.Spec.EphemeralContainers {
		containers = append(containers, c.Name)
	}
	return containers
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

type LogQuery struct {
	Container    string
	TailLines    int64
	SinceSeconds *int64
	SinceTime    time.Time
	UntilTime    time.Time
	Pod          string
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

func cnpgLogSourceLabel(role, container, selected string) string {
	if selected == "all" {
		if role == "" {
			return container
		}
		return role + " · " + container
	}
	return role
}

func cnpgLogsIntervalGone(lost int) string {
	if lost == 1 {
		return "Kubernetes keeps only the current and previous run of each container; 1 instance run that covered part of this interval was replaced and its lines are gone."
	}
	return fmt.Sprintf("Kubernetes keeps only the current and previous run of each container; %d instance runs that covered part of this interval were replaced and their lines are gone.", lost)
}

func cnpgSnapshotLogSources(pods []*corev1.Pod, selected string) []podlogs.Source {
	sources := []podlogs.Source{}
	for _, pod := range pods {
		for _, container := range cnpgLogContainers(pod, selected) {
			status := cnpgContainerStatus(pod, container)
			if status == nil {
				continue
			}
			if status.State.Running != nil || status.State.Terminated != nil {
				sources = append(sources, podlogs.NewSource(pod, container, false))
			} else if status.State.Waiting != nil && status.LastTerminationState.Terminated != nil {
				sources = append(sources, podlogs.NewSource(pod, container, true))
			}
		}
	}
	return sources
}

type cnpgStreamHandle struct {
	uid        types.UID
	cancel     context.CancelFunc
	follow     int
	generation int
	opened     atomic.Bool
}

type cnpgLogSourceFailure struct {
	key    string
	uid    types.UID
	follow int
	reason string
}

type cnpgCompletedLogRun struct {
	key    string
	uid    types.UID
	run    string
	follow int
}

// followCNPGContainerLogs reports whether it read the stream to its end, and
// returns a node's read/follow notice as an error rather than log output.
func followCNPGContainerLogs(ctx context.Context, client kubernetes.Interface, namespace, podName string, opts corev1.PodLogOptions, follow int, sourceLabel string, logCh chan<- podlogs.Entry, onOpen func()) (bool, error) {
	stream, err := client.CoreV1().Pods(namespace).GetLogs(podName, &opts).Stream(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[cnpg] Failed to follow logs for %s/%s/%s: %v", namespace, podName, opts.Container, err)
		}
		return false, nil
	}
	defer stream.Close()
	if onOpen != nil {
		onOpen()
	}
	reader := bufio.NewReader(stream)
	for {
		line, err := reader.ReadString('\n')
		if line = strings.TrimSuffix(line, "\n"); line != "" && (err == nil || err == io.EOF) {
			// The kubelet's notice for a container it no longer holds logs for
			// arrives as the stream's only line. It is not output.
			if followErr := k8score.LogFollowError(line); followErr != nil {
				return false, followErr
			}
			ts, content := podlogs.ParseLine(line)
			select {
			case logCh <- podlogs.Entry{Pod: podName, Container: opts.Container, Timestamp: ts, Content: content, SourceLabel: sourceLabel, Previous: opts.Previous, Follow: follow}:
			case <-ctx.Done():
				return false, nil
			}
		}
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				log.Printf("[cnpg] Failed to read logs for %s/%s/%s: %v", namespace, podName, opts.Container, err)
			}
			return err == io.EOF, nil
		}
	}
}

func (q LogQuery) keep(entry podlogs.Entry) bool {
	if q.SinceTime.IsZero() || entry.Timestamp == "" {
		return true
	}
	ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return true
	}
	return !ts.Before(q.SinceTime) && (q.UntilTime.IsZero() || !ts.After(q.UntilTime))
}

func ParseLogQuery(q url.Values, now time.Time) (LogQuery, error) {
	out := LogQuery{
		Container:    q.Get("container"),
		TailLines:    podlogs.ParseTailLines(q.Get("tailLines"), defaultLogTailLines),
		SinceSeconds: podlogs.ParseSinceSeconds(q.Get("sinceSeconds")),
		Pod:          q.Get("pod"),
	}
	if out.Container == "" {
		out.Container = defaultLogContainer
	}
	if raw := q.Get("sinceTime"); raw != "" {
		if q.Get("sinceSeconds") != "" {
			return out, errors.New("sinceSeconds and sinceTime are mutually exclusive")
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return out, fmt.Errorf("invalid sinceTime %q (expected RFC3339)", raw)
		}
		out.SinceTime = t
		// The pod log API takes whole seconds; round up and trim the overlap
		// from the entries afterwards.
		secs := max(int64(math.Ceil(now.Sub(t).Seconds())), 1)
		out.SinceSeconds = &secs
	}
	if raw := q.Get("untilTime"); raw != "" {
		if out.SinceTime.IsZero() {
			return out, errors.New("untilTime requires sinceTime")
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return out, fmt.Errorf("invalid untilTime %q (expected RFC3339)", raw)
		}
		if !t.After(out.SinceTime) {
			return out, errors.New("untilTime must be after sinceTime")
		}
		out.UntilTime = t
		// An interval is read from its start: the pod log API has no upper
		// bound, and a tail would return the lines nearest now instead.
		if q.Get("tailLines") == "" {
			out.TailLines = 0
		}
	}
	return out, nil
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

type LogTarget struct {
	reader          *Reader
	namespace, name string
	cache           *k8s.ResourceCache
	cluster         *unstructured.Unstructured
	pods            []*corev1.Pod
}

func (rd *Reader) PrepareLogs(ctx context.Context, namespace, name string, query LogQuery) (*LogTarget, error) {
	cache, cluster, err := rd.Observations.Cluster(ctx, namespace, name,
		auth.Grant{Resource: "pods", Verb: "list", Namespace: namespace},
		auth.Grant{Resource: "pods", Subresource: "log", Verb: "get", Namespace: namespace})
	if err != nil {
		return nil, err
	}
	instances, err := clusterInstancePods(cache, cluster)
	if err != nil {
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", namespace, name, err)
		return nil, &ReadFailure{http.StatusServiceUnavailable, "instance Pods unavailable: " + err.Error()}
	}
	pods, ok := selectCNPGLogPods(instances, query.Pod)
	if !ok {
		return nil, &ReadFailure{http.StatusBadRequest, "pod " + query.Pod + " is not an instance of CloudNativePG Cluster " + namespace + "/" + name}
	}
	return &LogTarget{reader: rd, namespace: namespace, name: name, cache: cache, cluster: cluster, pods: pods}, nil
}

func (target *LogTarget) RequireClient() error {
	if target.reader.Clients.Typed == nil {
		return &ReadFailure{http.StatusServiceUnavailable, "cluster client unavailable"}
	}
	return nil
}

func (target *LogTarget) currentCluster(ctx context.Context) (*unstructured.Unstructured, error) {
	objects, err := target.reader.Observations.DynamicList(ctx, target.cache, "Cluster", Group, target.namespace)
	return SelectCluster(objects, err, target.namespace, target.name)
}

func SelectCluster(objects []*unstructured.Unstructured, err error, namespace, name string) (*unstructured.Unstructured, error) {
	clusters, err := filterCNPGGroup(objects, err)
	if err != nil {
		return nil, err
	}
	for _, c := range clusters {
		if c.GetNamespace() == namespace && c.GetName() == name && c.GroupVersionKind().Group == Group {
			return c, nil
		}
	}
	return nil, nil
}

func (target *LogTarget) Snapshot(ctx context.Context, query LogQuery) (ClusterLogsResponse, error) {
	namespace, cluster, pods := target.namespace, target.cluster, target.pods
	resp := ClusterLogsResponse{
		UID:          cluster.GetUID(),
		Pods:         []podlogs.PodInfo{},
		Logs:         []podlogs.Entry{},
		CapturedAt:   time.Now().UTC().Format(time.RFC3339),
		EmptyMessage: logsEmptyMessage,
	}
	if len(pods) == 0 {
		resp.EmptyMessage = logsNoInstanceMessage
		return resp, nil
	}
	client := target.reader.Clients.Typed
	if client == nil {
		return ClusterLogsResponse{}, &ReadFailure{http.StatusServiceUnavailable, "cluster client unavailable"}
	}

	var snapshot podlogs.Snapshot
	lost := 0
	if query.UntilTime.IsZero() {
		snapshot = podlogs.CollectSources(ctx, client, namespace, cnpgSnapshotLogSources(pods, query.Container), query.TailLines, query.SinceSeconds, true)
	} else {
		var sources []podlogs.Source
		sources, lost = cnpgIntervalLogSources(pods, query.Container, query.SinceTime, query.UntilTime)
		snapshot = podlogs.CollectSources(ctx, client, namespace, sources, query.TailLines, query.SinceSeconds, true)
		snapshot.Notice = snapshot.Summarize(func(clip podlogs.Clip) bool {
			return clip.Last.IsZero() || clip.Last.Before(query.UntilTime)
		})
		resp.EmptyMessage = logsIntervalEmpty
	}
	shown := []*corev1.Pod{}
	sourceLabels := map[string]string{}
	for _, p := range pods {
		if !snapshot.SourcePods[p.Name] {
			continue
		}
		shown = append(shown, p)
		if role := instanceRole(p); role != "" {
			sourceLabels[p.Name] = cnpgInstanceSourceLabel(p, role)
		}
	}
	for _, entry := range snapshot.Logs {
		if !query.keep(entry) {
			continue
		}
		entry.SourceLabel = cnpgLogSourceLabel(sourceLabels[entry.Pod], entry.Container, query.Container)
		if entry.Previous && entry.SourceLabel != "" {
			entry.SourceLabel += " · previous run"
		}
		annotateCNPGLogEntry(&entry)
		resp.Logs = append(resp.Logs, entry)
	}
	podlogs.Sort(resp.Logs)
	resp.Pods = podlogs.BuildPodInfos(shown)
	resp.Notice = snapshot.Notice
	if lost > 0 {
		resp.Notice = strings.TrimSpace(cnpgLogsIntervalGone(lost) + " " + resp.Notice)
	}
	if len(sourceLabels) > 0 {
		resp.SourceLabels = sourceLabels
	}
	return resp, nil
}

func (target *LogTarget) Follow(parent context.Context, query LogQuery, send func(string, any)) {
	namespace, name, cluster, pods, cache, client := target.namespace, target.name, target.cluster, target.pods, target.cache, target.reader.Clients.Typed
	clusterDone := target.reader.Clients.ClusterDone
	uid := cluster.GetUID()
	send("connected", map[string]any{
		"cluster": name, "namespace": namespace, "uid": uid, "pods": podlogs.BuildPodInfos(pods),
	})

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	logCh := make(chan podlogs.Entry, 1000)
	unavailableCh := make(chan cnpgLogSourceFailure)
	completedCh := make(chan cnpgCompletedLogRun)
	unavailable := podlogs.UnavailableSources{}
	sendUnavailable := func() { send("notice", map[string]string{"notice": unavailable.Notice()}) }
	var active sync.Map
	completed := map[string]cnpgCompletedLogRun{}
	roles := map[string]string{}
	cursors := map[string]*podlogs.StreamCursor{}
	stopPod := func(podName string) {
		active.Range(func(key, value any) bool {
			if strings.HasPrefix(key.(string), podName+"/") {
				value.(*cnpgStreamHandle).cancel()
				active.CompareAndDelete(key, value)
			}
			return true
		})
		for key := range cursors {
			if strings.HasPrefix(key, podName+"/") {
				delete(cursors, key)
			}
		}
		for key := range completed {
			if strings.HasPrefix(key, podName+"/") {
				delete(completed, key)
			}
		}
		delete(roles, podName)
	}
	follows := 0
	generation := 0
	start := func(pods []*corev1.Pod) {
		for _, pod := range pods {
			if role := instanceRole(pod); role != "" {
				roles[pod.Name] = cnpgInstanceSourceLabel(pod, role)
			}
			for _, c := range cnpgLogContainers(pod, query.Container) {
				status := cnpgContainerStatus(pod, c)
				if status == nil {
					continue
				}
				previous := status.State.Waiting != nil && status.LastTerminationState.Terminated != nil
				if status.State.Running == nil && status.State.Terminated == nil && !previous {
					continue
				}
				key := pod.Name + "/" + c
				run := fmt.Sprintf("%s/%d", status.ContainerID, status.RestartCount)
				if previous {
					run = fmt.Sprintf("%s/%d/previous", status.LastTerminationState.Terminated.ContainerID, status.RestartCount)
				}
				terminated := status.State.Terminated != nil || previous
				if read, ok := completed[key]; terminated && ok && read.uid == pod.UID && read.run == run {
					continue
				}
				if value, exists := active.Load(key); exists {
					handle := value.(*cnpgStreamHandle)
					if handle.uid == pod.UID {
						continue
					}
					handle.cancel()
				}
				follows++
				follow := follows
				opts := podlogs.CursorFor(cursors, key, pod.UID, follow).RestartOptions(c, query.TailLines, query.SinceSeconds)
				if previous {
					opts.Previous = true
					opts.Follow = false
				}
				sourceLabel := cnpgLogSourceLabel(roles[pod.Name], c, query.Container)
				if previous && sourceLabel != "" {
					sourceLabel += " · previous run"
				}
				streamCtx, streamCancel := context.WithCancel(ctx)
				handle := &cnpgStreamHandle{uid: pod.UID, cancel: streamCancel, follow: follow, generation: generation}
				active.Store(key, handle)
				go func(podName, key string) {
					defer streamCancel()
					defer active.CompareAndDelete(key, handle)
					done, err := followCNPGContainerLogs(streamCtx, client, namespace, podName, opts, follow, sourceLabel, logCh, func() { handle.opened.Store(true) })
					active.CompareAndDelete(key, handle)
					if err != nil {
						select {
						case unavailableCh <- cnpgLogSourceFailure{key: key, uid: handle.uid, follow: follow, reason: err.Error()}:
						case <-streamCtx.Done():
						}
						return
					}
					if done && terminated && streamCtx.Err() == nil {
						select {
						case completedCh <- cnpgCompletedLogRun{key: key, uid: handle.uid, run: run, follow: follow}:
						case <-streamCtx.Done():
						}
					}
				}(pod.Name, key)
			}
		}
	}
	start(pods)

	known := map[string]types.UID{}
	for _, p := range pods {
		known[p.Name] = p.UID
	}
	ticker := time.NewTicker(logDiscoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-clusterDone:
			send("end", map[string]string{"reason": "context changed"})
			return
		case failure := <-unavailableCh:
			// A stream canceled during pod removal can still deliver its report.
			key := failure.key
			podName, _, _ := strings.Cut(key, "/")
			if uid, seen := known[podName]; seen && uid == failure.uid && unavailable.Add(key, failure.follow, failure.reason) {
				sendUnavailable()
			}
		case read := <-completedCh:
			podName, _, _ := strings.Cut(read.key, "/")
			if uid, seen := known[podName]; seen && uid == read.uid {
				completed[read.key] = read
				if unavailable.Remove(read.key, read.follow) {
					sendUnavailable()
				}
			}
		case entry := <-logCh:
			key := entry.Pod + "/" + entry.Container
			cursor := cursors[key]
			fromCurrentPod := cursor != nil && cursor.FromCurrentPod(entry)
			if cursor != nil && !cursor.Admit(entry) {
				continue
			}
			if !query.keep(entry) {
				continue
			}
			if fromCurrentPod {
				entry.SourceLabel = cnpgLogSourceLabel(roles[entry.Pod], entry.Container, query.Container)
				if entry.Previous && entry.SourceLabel != "" {
					entry.SourceLabel += " · previous run"
				}
			}
			annotateCNPGLogEntry(&entry)
			send("log", entry)
		case <-ticker.C:
			generation++
			current, err := target.currentCluster(ctx)
			if err != nil {
				continue
			}
			if current == nil || current.GetUID() != uid {
				send("end", map[string]string{"reason": "cluster deleted"})
				return
			}
			all, err := clusterInstancePods(cache, current)
			if err != nil {
				continue
			}
			currentPods, _ := selectCNPGLogPods(all, query.Pod)
			present := map[string]bool{}
			for _, p := range currentPods {
				present[p.Name] = true
				if previousUID, seen := known[p.Name]; !seen || previousUID != p.UID {
					if seen {
						stopPod(p.Name)
						if unavailable.RemovePod(p.Name) {
							sendUnavailable()
						}
						send("pod_removed", map[string]string{"pod": p.Name, "reason": "replaced"})
					}
					known[p.Name] = p.UID
					send("pod_added", map[string]any{"pods": []podlogs.PodInfo{podlogs.BuildPodInfo(p, time.Now())}})
				}
			}
			for podName := range known {
				if present[podName] {
					continue
				}
				delete(known, podName)
				stopPod(podName)
				if unavailable.RemovePod(podName) {
					sendUnavailable()
				}
				send("pod_removed", map[string]string{"pod": podName, "reason": "terminated"})
			}
			active.Range(func(key, value any) bool {
				handle := value.(*cnpgStreamHandle)
				podName, _, _ := strings.Cut(key.(string), "/")
				if known[podName] == handle.uid && handle.generation < generation && handle.opened.Load() && unavailable.Remove(key.(string), handle.follow) {
					sendUnavailable()
				}
				return true
			})
			start(currentPods)
		}
	}
}
