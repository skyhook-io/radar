package podlogs

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type Entry struct {
	Pod         string `json:"pod"`
	Container   string `json:"container"`
	Timestamp   string `json:"timestamp"`
	Content     string `json:"content"`
	SourceLabel string `json:"sourceLabel,omitempty"`
	// Previous marks a line from the container run before its last restart.
	Previous bool `json:"previous,omitempty"`
	// Parsed from a structured line by sources that know their log format.
	Level   string `json:"level,omitempty"`
	Logger  string `json:"logger,omitempty"`
	Message string `json:"message,omitempty"`
}

// CollectPods fetches logs from all pods concurrently. Non-nil even
// when nothing is retrievable (e.g. every pod is crashlooping) — a nil slice
// marshals as JSON null and consumers expect an array.
type Snapshot struct {
	SourcePods map[string]bool
	Logs       []Entry
	Notice     string
	// Clipped lists the sources that reached maxSnapshotSourceBytes, with the
	// timestamp of the last line kept from each, so a caller filtering by time
	// can tell whether the clip cut into its window.
	Clipped []Clip
	shown   int
	total   int
	errors  []string
}

type Clip struct {
	Pod, Container string
	Previous       bool
	Last           time.Time
}

// Source is one container run to read: the current run, or with
// Previous the one before the last restart.
type Source struct {
	Pod, Container string
	Previous       bool
	running        bool
	created        time.Time
}

func (s Source) key() string {
	if s.Previous {
		return s.Pod + "/" + s.Container + " (previous run)"
	}
	return s.Pod + "/" + s.Container
}

const maxSnapshotSources = 40

const maxSnapshotSourceBytes int64 = 64 * 1024

func CollectPods(ctx context.Context, client kubernetes.Interface, namespace string, pods []*corev1.Pod, container string, tailLines int64, sinceSeconds *int64, bounded bool) Snapshot {
	sources := []Source{}
	for _, pod := range pods {
		for _, c := range k8s.GetContainersForPod(pod, container, true) {
			if bounded {
				started := false
				for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses, pod.Status.EphemeralContainerStatuses} {
					for _, status := range statuses {
						if status.Name == c && (status.State.Running != nil || status.State.Terminated != nil) {
							started = true
						}
					}
				}
				if !started {
					continue
				}
			}
			sources = append(sources, NewSource(pod, c, false))
		}
	}
	return CollectSources(ctx, client, namespace, sources, tailLines, sinceSeconds, bounded)
}

func NewSource(pod *corev1.Pod, container string, previous bool) Source {
	return Source{Pod: pod.Name, Container: container, Previous: previous, running: pod.Status.Phase == corev1.PodRunning, created: pod.CreationTimestamp.Time}
}

// CollectSources reads each source concurrently; bounded caps the source
// count, bytes per source and overall time.
func CollectSources(ctx context.Context, client kubernetes.Interface, namespace string, sources []Source, tailLines int64, sinceSeconds *int64, bounded bool) Snapshot {
	sort.Slice(sources, func(i, j int) bool {
		if bounded && sources[i].running != sources[j].running {
			return sources[i].running
		}
		if bounded && !sources[i].created.Equal(sources[j].created) {
			return sources[i].created.After(sources[j].created)
		}
		if sources[i].Pod != sources[j].Pod {
			return sources[i].Pod < sources[j].Pod
		}
		if sources[i].Container != sources[j].Container {
			return sources[i].Container < sources[j].Container
		}
		return sources[i].Previous && !sources[j].Previous
	})
	total := len(sources)
	if bounded && len(sources) > maxSnapshotSources {
		sources = sources[:maxSnapshotSources]
	}
	if bounded {
		tailLines = min(tailLines, 1000)
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	result := Snapshot{Logs: []Entry{}, SourcePods: map[string]bool{}, shown: len(sources), total: total}
	for _, src := range sources {
		result.SourcePods[src.Pod] = true
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	concurrency := len(sources)
	if bounded {
		concurrency = min(concurrency, 8)
	}
	sem := make(chan struct{}, concurrency)
	for _, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				result.errors = append(result.errors, src.key()+": request cancelled")
				mu.Unlock()
				return
			}
			entries, clipped, err := fetchPodContainerLogs(ctx, client, namespace, src.Pod, src.Container, tailLines, sinceSeconds, bounded, src.Previous)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				result.errors = append(result.errors, src.key()+": "+err.Error())
			}
			if clipped {
				clip := Clip{Pod: src.Pod, Container: src.Container, Previous: src.Previous}
				if n := len(entries); n > 0 {
					clip.Last, _ = time.Parse(time.RFC3339Nano, entries[n-1].Timestamp)
				}
				result.Clipped = append(result.Clipped, clip)
			}
			result.Logs = append(result.Logs, entries...)
		}()
	}
	wg.Wait()
	sort.Strings(result.errors)
	result.Notice = result.Summarize(func(Clip) bool { return true })
	return result
}

// Summarize summarizes what the snapshot could not show. counts decides which
// clipped sources are worth reporting — a clip past the caller's window cut
// nothing the caller asked for.
func (s Snapshot) Summarize(counts func(Clip) bool) string {
	notices := []string{}
	if s.total > s.shown {
		notices = append(notices, fmt.Sprintf("Showing %d of %d container sources. Narrow the scope to see other sources.", s.shown, s.total))
	}
	truncated := 0
	for _, clip := range s.Clipped {
		if counts(clip) {
			truncated++
		}
	}
	if truncated > 0 {
		notices = append(notices, countNoun(truncated, "source", "sources")+" reached the 64 KiB snapshot limit.")
	}
	if len(s.errors) > 0 {
		notices = append(notices, fmt.Sprintf("%s could not be read: %s", countNoun(len(s.errors), "source", "sources"), strings.Join(s.errors[:min(3, len(s.errors))], "; ")))
	}
	return strings.Join(notices, " ")
}

// countNoun reads "1 source", "2 sources".
func countNoun(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func fetchPodContainerLogs(ctx context.Context, client kubernetes.Interface, namespace, podName, containerName string, tailLines int64, sinceSeconds *int64, bounded, previous bool) ([]Entry, bool, error) {
	var limit *int64
	if bounded {
		n := maxSnapshotSourceBytes + 1
		limit = &n
	}
	// Zero reads from the start of the since window instead of its tail, so a
	// bounded read of a past interval returns that interval's first lines.
	var tail *int64
	if tailLines > 0 {
		tail = &tailLines
	}
	stream, err := k8score.GetContainerLogs(ctx, client, namespace, podName, containerName, k8score.LogOptions{
		TailLines: tail, SinceSeconds: sinceSeconds, Timestamps: true, LimitBytes: limit, Previous: previous,
	})
	if err != nil {
		return nil, false, err
	}
	defer stream.Close()
	var reader io.Reader = stream
	if limit != nil {
		reader = io.LimitReader(stream, *limit)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, err
	}
	clipped := bounded && int64(len(content)) > maxSnapshotSourceBytes
	if clipped {
		content = content[:maxSnapshotSourceBytes]
		if last := strings.LastIndexByte(string(content), '\n'); last >= 0 {
			content = content[:last+1]
		} else {
			content = nil
		}
	}
	entries := []Entry{}
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" {
			continue
		}
		ts, text := ParseLine(line)
		entries = append(entries, Entry{Pod: podName, Container: containerName, Timestamp: ts, Content: text, Previous: previous})
	}
	return entries, clipped, nil
}

// Sort sorts log entries by timestamp using efficient sort
func Sort(logs []Entry) {
	sort.SliceStable(logs, func(i, j int) bool {
		left, le := time.Parse(time.RFC3339Nano, logs[i].Timestamp)
		right, re := time.Parse(time.RFC3339Nano, logs[j].Timestamp)
		if le == nil && re == nil && !left.Equal(right) {
			return left.Before(right)
		}
		if (le == nil) != (re == nil) {
			return le != nil
		}
		if le != nil && logs[i].Timestamp != logs[j].Timestamp {
			return logs[i].Timestamp < logs[j].Timestamp
		}
		if logs[i].Pod != logs[j].Pod {
			return logs[i].Pod < logs[j].Pod
		}
		return logs[i].Container < logs[j].Container
	})
}

// ParseLine extracts timestamp from a log line (format: 2024-01-20T10:30:00.123456789Z content)
func ParseLine(line string) (timestamp, content string) {
	// K8s timestamps are in RFC3339Nano format at the start of the line
	if len(line) > 30 && line[4] == '-' && line[7] == '-' && line[10] == 'T' {
		// Find the space after timestamp
		spaceIdx := strings.Index(line, " ")
		if spaceIdx > 20 && spaceIdx < 40 {
			return line[:spaceIdx], line[spaceIdx+1:]
		}
	}
	return "", line
}
