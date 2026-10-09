package cnpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/podlogs"
)

func TestPrepareLogsRequiresInventoryAndLogGrantsBeforeLookup(t *testing.T) {
	denied := &ReadFailure{Status: 403, Message: "no access"}
	reader := &Reader{Observations: Observations{Cluster: func(_ context.Context, namespace, name string, grants ...auth.Grant) (*k8s.ResourceCache, *unstructured.Unstructured, error) {
		if namespace != "pg" || name != "orders" || len(grants) != 2 {
			t.Fatalf("unexpected target or grants: %s/%s %+v", namespace, name, grants)
		}
		if grants[0] != (auth.Grant{Resource: "pods", Verb: "list", Namespace: "pg"}) || grants[1] != (auth.Grant{Resource: "pods", Subresource: "log", Verb: "get", Namespace: "pg"}) {
			t.Fatalf("wrong operations: %+v", grants)
		}
		return nil, nil, denied
	}}}
	if target, err := reader.PrepareLogs(context.Background(), "pg", "orders", LogQuery{}); target != nil || !errors.Is(err, denied) {
		t.Fatalf("denied preparation = %+v, %v", target, err)
	}
}

func TestLogSnapshotWithoutInstancesKeepsArrayWireShape(t *testing.T) {
	cluster := &unstructured.Unstructured{}
	cluster.SetUID("cluster-uid")
	target := &LogTarget{reader: &Reader{}, cluster: cluster}
	response, err := target.Snapshot(context.Background(), LogQuery{})
	if err != nil || response.UID != "cluster-uid" || response.Pods == nil || response.Logs == nil || response.EmptyMessage != logsNoInstanceMessage {
		t.Fatalf("empty snapshot = %+v, %v", response, err)
	}
	if err := target.RequireClient(); err == nil {
		t.Fatal("a stream must require a client even before any instances exist")
	}
}

func TestAnnotateCNPGLogEntry(t *testing.T) {
	cases := []struct {
		name, content, level, logger, message string
	}{
		{
			name:    "postgres record",
			content: `{"level":"info","ts":"2026-09-28T14:19:58.123Z","logger":"postgres","msg":"record","record":{"error_severity":"FATAL","message":"password authentication failed","log_time":"2026-09-28 14:19:58.123 UTC"}}`,
			level:   "FATAL", logger: "postgres", message: "password authentication failed",
		},
		{
			name:    "instance manager error",
			content: `{"level":"error","ts":"2026-09-28T14:19:58Z","logger":"barman-cloud-wal-archive","msg":"Error invoking barman-cloud-wal-archive","error":"exit status 4"}`,
			level:   "ERROR", logger: "barman-cloud-wal-archive", message: "Error invoking barman-cloud-wal-archive: exit status 4",
		},
		{
			name:    "structured error",
			content: `{"level":"error","msg":"failed","error":{"code":2}}`,
			level:   "ERROR", message: `failed: {"code":2}`,
		},
		{name: "plain text", content: "LOG:  database system is ready"},
		{name: "broken json", content: `{"level":"info"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := podlogs.Entry{Content: tc.content}
			annotateCNPGLogEntry(&entry)
			if entry.Level != tc.level || entry.Logger != tc.logger || entry.Message != tc.message || entry.Content != tc.content {
				t.Fatalf("got level=%q logger=%q message=%q content-changed=%v", entry.Level, entry.Logger, entry.Message, entry.Content != tc.content)
			}
		})
	}
	raw, _ := json.Marshal(podlogs.Entry{Pod: "p", Content: "x"})
	if strings.Contains(string(raw), "level") || strings.Contains(string(raw), "message") {
		t.Fatalf("unparsed entries grew fields: %s", raw)
	}
}

func TestParseCNPGLogQuery(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	req := logValues("/?sinceTime=2026-09-28T11:59:00.5Z")
	if _, err := ParseLogQuery(req, now); err != nil {
		t.Fatalf("fractional RFC3339 rejected: %v", err)
	}
	req = logValues("/?sinceTime=2026-09-28T11:58:30Z")
	q, err := ParseLogQuery(req, now)
	if err != nil || q.SinceSeconds == nil || *q.SinceSeconds != 90 || q.Container != "postgres" || q.TailLines != 200 {
		t.Fatalf("query = %+v err=%v", q, err)
	}
	if q.keep(podlogs.Entry{Timestamp: "2026-09-28T11:58:29.9Z"}) || !q.keep(podlogs.Entry{Timestamp: "2026-09-28T11:58:30Z"}) {
		t.Fatal("sinceTime overlap not trimmed")
	}
	req = logValues("/?sinceTime=2026-09-28T11:58:30Z&sinceSeconds=5")
	if _, err := ParseLogQuery(req, now); err == nil {
		t.Fatal("sinceTime with sinceSeconds accepted")
	}
}

func TestCNPGStreamCursorResumesWithoutReplay(t *testing.T) {
	var c cnpgStreamCursor
	first := c.restartOptions("postgres", 200, nil)
	if first.TailLines == nil || *first.TailLines != 200 || first.SinceTime != nil || !first.Follow || !first.Timestamps || first.Container != "postgres" {
		t.Fatalf("first start = %+v", first)
	}
	line := func(ts, content string) podlogs.Entry {
		return podlogs.Entry{Timestamp: ts, Content: content}
	}
	for _, e := range []podlogs.Entry{line("2026-09-28T14:00:00.1Z", "a"), line("2026-09-28T14:00:05.7Z", "b"), line("2026-09-28T14:00:05.7Z", "c")} {
		if !c.admit(e) {
			t.Fatalf("fresh line %+v rejected", e)
		}
	}

	restart := c.restartOptions("postgres", 200, nil)
	if restart.TailLines != nil || restart.SinceSeconds != nil || restart.SinceTime == nil ||
		!restart.SinceTime.Time.Equal(time.Date(2026, 9, 28, 14, 0, 5, 0, time.UTC)) {
		t.Fatalf("restart = %+v, want sinceTime at the last delivered second and no tail", restart)
	}

	// The resumed follow replays the boundary second.
	replayed := []podlogs.Entry{line("2026-09-28T14:00:05.2Z", "earlier in the second"), line("2026-09-28T14:00:05.7Z", "b"), line("2026-09-28T14:00:05.7Z", "c")}
	for _, e := range replayed {
		if c.admit(e) {
			t.Errorf("replayed line %+v admitted", e)
		}
	}
	for _, e := range []podlogs.Entry{line("2026-09-28T14:00:05.7Z", "d"), line("2026-09-28T14:00:06Z", "e")} {
		if !c.admit(e) {
			t.Errorf("new line %+v rejected", e)
		}
	}
	if c.admit(line("2026-09-28T14:00:05.7Z", "d")) {
		t.Error("line before the new last timestamp admitted")
	}
}

func TestCNPGIntervalLogSources(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 9, 29, 23, m, 0, 0, time.UTC) }
	since, until := at(20), at(30)
	pod := func(status corev1.ContainerStatus) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pg-1", Namespace: "ns"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres"}}}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{status}
		return p
	}
	waiting := cnpgRestartedStatus(at(0), at(10), at(40), 3)
	waiting.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	cases := []struct {
		name              string
		status            corev1.ContainerStatus
		current, previous bool
		lost              int
	}{
		{"restarted after the interval reads only the previous run", cnpgRestartedStatus(at(45), at(10), at(40), 1), false, true, 0},
		{"restart inside the interval reads both runs", cnpgRestartedStatus(at(25), at(10), at(24), 1), true, true, 0},
		{"running since before the interval reads the current run", cnpgRestartedStatus(at(5), at(0), at(4), 1), true, false, 0},
		{"previous run ended before the interval is skipped", cnpgRestartedStatus(at(25), at(0), at(15), 2), true, false, 0},
		{"older runs than the kept two covered the interval", cnpgRestartedStatus(at(45), at(28), at(44), 5), false, true, 1},
		{"crash-looping container reads its previous run", waiting, false, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sources, lost := cnpgIntervalLogSources([]*corev1.Pod{pod(tc.status)}, "postgres", since, until)
			var current, previous bool
			for _, s := range sources {
				if s.Previous {
					previous = true
				} else {
					current = true
				}
			}
			if current != tc.current || previous != tc.previous || lost != tc.lost {
				t.Fatalf("current=%v previous=%v lost=%d, want %v %v %d", current, previous, lost, tc.current, tc.previous, tc.lost)
			}
		})
	}
}

func TestCNPGLogsContainerSelection(t *testing.T) {
	q, err := ParseLogQuery(logValues("/?container=all"), time.Now())
	if err != nil || q.Container != "all" {
		t.Fatalf("%+v %v", q, err)
	}
	q, err = ParseLogQuery(logValues("/"), time.Now())
	if err != nil || q.Container != "postgres" {
		t.Fatalf("default: %+v %v", q, err)
	}
	p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres"}}, InitContainers: []corev1.Container{{Name: "sidecar"}}}}
	if got := strings.Join(cnpgLogContainers(p, "all"), ","); got != "postgres,sidecar" {
		t.Fatal(got)
	}
	if got := strings.Join(cnpgLogContainers(p, "sidecar"), ","); got != "sidecar" {
		t.Fatal(got)
	}
	if len(cnpgLogContainers(p, "no-such-container")) != 0 {
		t.Fatal("selected unknown container")
	}
}

func TestCNPGLogsWaitingSources(t *testing.T) {
	for _, retained := range []bool{true, false} {
		t.Run(fmt.Sprintf("retained=%t", retained), func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pg-1"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres"}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "postgres", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}}
			if retained {
				pod.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ContainerID: "containerd://crash", ExitCode: 1}
			}
			sources := cnpgSnapshotLogSources([]*corev1.Pod{pod}, "postgres")
			if !retained {
				if len(sources) != 0 {
					t.Fatalf("read container that never started: %+v", sources)
				}
				return
			}
			if len(sources) != 1 || !sources[0].Previous {
				t.Fatalf("missing previous-run source: %+v", sources)
			}
		})
	}
}

func TestParseCNPGLogQueryInterval(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	q, err := ParseLogQuery(logValues("/?sinceTime=2026-09-28T11:00:00Z&untilTime=2026-09-28T11:05:00Z"), now)
	if err != nil || q.TailLines != 0 || q.SinceSeconds == nil || *q.SinceSeconds != 3600 {
		t.Fatalf("q = %+v err = %v", q, err)
	}
	if !q.keep(podlogs.Entry{Timestamp: "2026-09-28T11:05:00Z"}) || q.keep(podlogs.Entry{Timestamp: "2026-09-28T11:05:00.1Z"}) || q.keep(podlogs.Entry{Timestamp: "2026-09-28T10:59:59Z"}) {
		t.Fatal("interval bounds not applied")
	}
	for _, bad := range []string{"/?untilTime=2026-09-28T11:05:00Z", "/?sinceTime=2026-09-28T11:05:00Z&untilTime=2026-09-28T11:00:00Z", "/?sinceTime=2026-09-28T11:00:00Z&untilTime=x"} {
		if _, err := ParseLogQuery(logValues(bad), now); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func cnpgRestartedStatus(currentStart, prevStart, prevEnd time.Time, restarts int32) corev1.ContainerStatus {
	status := corev1.ContainerStatus{
		Name: "postgres", RestartCount: restarts,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(currentStart)}},
	}
	if restarts > 0 {
		status.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{StartedAt: metav1.NewTime(prevStart), FinishedAt: metav1.NewTime(prevEnd)}
	}
	return status
}
func logValues(raw string) url.Values {
	values, err := url.ParseQuery(strings.TrimPrefix(raw, "/?"))
	if err != nil {
		panic(err)
	}
	return values
}
