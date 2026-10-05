package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
)

func seedCNPGLogCluster(t *testing.T, ns string) {
	t.Helper()
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		withUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", ns, "pg-orders", map[string]any{"instances": int64(2)}, nil), "orders-uid"),
		cnpgObj("cluster.x-k8s.io/v1beta1", "Cluster", ns, "capi-only", nil, nil),
	)
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: "orders-uid", Controller: boolPtr(true)}
	stale := owner
	stale.UID = "previous-incarnation"
	replica := cnpgPod(ns, "pg-orders-2", "pg-orders", owner)
	replica.Labels["cnpg.io/instanceRole"] = "replica"
	seedCNPGPods(t,
		cnpgPod(ns, "pg-orders-1", "pg-orders", owner),
		replica,
		cnpgPod(ns, "pg-orders-impostor", "pg-orders"),
		cnpgPod(ns, "pg-orders-orphan", "pg-orders", stale),
	)
}

func getCNPGLogs(t *testing.T, path string) (int, CNPGClusterLogsResponse, string) {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out CNPGClusterLogsResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, body)
		}
	}
	return resp.StatusCode, out, string(body)
}

// useLogServer points Radar's client at an apiserver that serves one JSON log
// line per Pod, so the handler's merge and parse run end to end.
func useLogServer(t *testing.T) {
	t.Helper()
	apiserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 2 || parts[len(parts)-1] != "log" {
			http.NotFound(w, r)
			return
		}
		pod := parts[len(parts)-2]
		fmt.Fprintf(w, "2026-09-28T14:19:58.5Z {\"level\":\"info\",\"logger\":\"postgres\",\"msg\":\"record\",\"record\":{\"error_severity\":\"LOG\",\"message\":\"hello from %s\"}}\n", pod)
	}))
	t.Cleanup(apiserver.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: apiserver.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
}

func TestCNPGClusterLogs_OnlyValidatedInstancesContribute(t *testing.T) {
	seedCNPGLogCluster(t, "pglogs")
	useLogServer(t)

	status, got, body := getCNPGLogs(t, "/api/cnpg/clusters/pglogs/pg-orders/logs")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if got.UID != "orders-uid" || got.CapturedAt == "" || got.EmptyMessage == "" {
		t.Fatalf("envelope = %+v", got)
	}
	var names []string
	for _, p := range got.Pods {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "pg-orders-1,pg-orders-2" {
		t.Fatalf("pods = %v, want only the owned instances", names)
	}
	for _, entry := range got.Logs {
		if entry.Pod != "pg-orders-1" && entry.Pod != "pg-orders-2" {
			t.Errorf("log from a non-instance Pod: %+v", entry)
		}
		if entry.Container != "postgres" {
			t.Errorf("container = %q, want the postgres default", entry.Container)
		}
	}
	if len(got.Logs) != 2 {
		t.Fatalf("logs = %+v, want one line per instance", got.Logs)
	}
	for _, entry := range got.Logs {
		if entry.Level != "LOG" || entry.Logger != "postgres" || entry.Message != "hello from "+entry.Pod || !strings.HasPrefix(entry.Content, "{") {
			t.Errorf("parsed entry = %+v", entry)
		}
	}
	if got.SourceLabels["pg-orders-1"] != "primary 1" || got.SourceLabels["pg-orders-2"] != "replica 2" {
		t.Errorf("sourceLabels = %v", got.SourceLabels)
	}

	status, got, body = getCNPGLogs(t, "/api/cnpg/clusters/pglogs/pg-orders/logs?pod=pg-orders-2")
	if status != http.StatusOK || len(got.Pods) != 1 || got.Pods[0].Name != "pg-orders-2" {
		t.Fatalf("pod filter: status=%d pods=%+v body=%s", status, got.Pods, body)
	}
	for _, bad := range []string{"pg-orders-impostor", "pg-orders-orphan", "nope"} {
		if status, _, _ := getCNPGLogs(t, "/api/cnpg/clusters/pglogs/pg-orders/logs?pod="+bad); status != http.StatusBadRequest {
			t.Errorf("pod=%s: status = %d, want 400", bad, status)
		}
	}
	if status, _, _ := getCNPGLogs(t, "/api/cnpg/clusters/pglogs/pg-orders/logs?sinceTime=yesterday"); status != http.StatusBadRequest {
		t.Errorf("bad sinceTime: status = %d, want 400", status)
	}
}

func TestCNPGClusterLogs_NotFound(t *testing.T) {
	seedCNPGLogCluster(t, "pglogs404")
	for _, path := range []string{
		"/api/cnpg/clusters/pglogs404/missing/logs",
		"/api/cnpg/clusters/pglogs404/capi-only/logs",
		"/api/cnpg/clusters/elsewhere/pg-orders/logs",
		"/api/cnpg/clusters/pglogs404/missing/logs/stream",
	} {
		if status, _, body := getCNPGLogs(t, path); status != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (%s)", path, status, body)
		}
	}
}

func TestCNPGClusterLogs_Authorization(t *testing.T) {
	seedCNPGLogCluster(t, "pglogsauth")
	env := newAuthTestServer(t)
	for _, u := range []struct {
		name     string
		clusters bool
	}{{"no-clusters", false}, {"no-logs", true}} {
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"pglogsauth"}}
		perms.SetCanI("get", cnpgGroup, "clusters", "pglogsauth", u.clusters)
		allow(perms, "", "pods", "pglogsauth", true)
		env.srv.permCache.Set(u.name, nil, perms)
	}
	for _, tc := range []struct{ user, path, want string }{
		{"no-clusters", "/api/cnpg/clusters/pglogsauth/pg-orders/logs", "clusters.postgresql.cnpg.io"},
		{"no-clusters", "/api/cnpg/clusters/pglogsauth/missing/logs", "clusters.postgresql.cnpg.io"},
		{"no-logs", "/api/cnpg/clusters/pglogsauth/pg-orders/logs", "get pods/log"},
		{"no-logs", "/api/cnpg/clusters/pglogsauth/pg-orders/logs/stream", "get pods/log"},
		{"no-logs", "/api/cnpg/clusters/pglogsauth/pg-orders/logs?container=all", "get pods/log"},
		{"no-logs", "/api/cnpg/clusters/pglogsauth/pg-orders/logs/stream?container=all", "get pods/log"},
	} {
		resp := env.authGet(t, tc.path, tc.user, "")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), tc.want) {
			t.Errorf("%s %s: status=%d body=%s, want 403 naming %q", tc.user, tc.path, resp.StatusCode, body, tc.want)
		}
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
			entry := workloadLogEntry{Content: tc.content}
			annotateCNPGLogEntry(&entry)
			if entry.Level != tc.level || entry.Logger != tc.logger || entry.Message != tc.message || entry.Content != tc.content {
				t.Fatalf("got level=%q logger=%q message=%q content-changed=%v", entry.Level, entry.Logger, entry.Message, entry.Content != tc.content)
			}
		})
	}
	raw, _ := json.Marshal(workloadLogEntry{Pod: "p", Content: "x"})
	if strings.Contains(string(raw), "level") || strings.Contains(string(raw), "message") {
		t.Fatalf("unparsed entries grew fields: %s", raw)
	}
}

func TestParseCNPGLogQuery(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	req := httptest.NewRequest("GET", "/?sinceTime=2026-09-28T11:59:00.5Z", nil)
	if _, err := parseCNPGLogQuery(req, now); err != nil {
		t.Fatalf("fractional RFC3339 rejected: %v", err)
	}
	req = httptest.NewRequest("GET", "/?sinceTime=2026-09-28T11:58:30Z", nil)
	q, err := parseCNPGLogQuery(req, now)
	if err != nil || q.sinceSeconds == nil || *q.sinceSeconds != 90 || q.container != "postgres" || q.tailLines != 200 {
		t.Fatalf("query = %+v err=%v", q, err)
	}
	if q.keep(workloadLogEntry{Timestamp: "2026-09-28T11:58:29.9Z"}) || !q.keep(workloadLogEntry{Timestamp: "2026-09-28T11:58:30Z"}) {
		t.Fatal("sinceTime overlap not trimmed")
	}
	req = httptest.NewRequest("GET", "/?sinceTime=2026-09-28T11:58:30Z&sinceSeconds=5", nil)
	if _, err := parseCNPGLogQuery(req, now); err == nil {
		t.Fatal("sinceTime with sinceSeconds accepted")
	}
}

func useMemoryTimeline(t *testing.T) timeline.EventStore {
	t.Helper()
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 1000}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})
	return timeline.GetStore()
}

type activityRow struct {
	id, apiVersion, kind, name, uid string
	source                          timeline.EventSource
	eventType                       timeline.EventType
	age                             time.Duration
	labels                          map[string]string
	owner                           *timeline.OwnerInfo
}

func seedActivity(t *testing.T, store timeline.EventStore, ns string, rows ...activityRow) {
	t.Helper()
	now := time.Now()
	for _, r := range rows {
		source, eventType := r.source, r.eventType
		if source == "" {
			source = timeline.SourceInformer
		}
		if eventType == "" {
			eventType = timeline.EventTypeUpdate
		}
		e := timeline.TimelineEvent{
			ID: r.id, Timestamp: now.Add(-r.age), Source: source, Kind: r.kind, APIVersion: r.apiVersion,
			Namespace: ns, Name: r.name, UID: r.uid, EventType: eventType, Labels: r.labels, Owner: r.owner,
			ClusterContext: k8s.ActiveClusterContext(),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("append %s: %v", r.id, err)
		}
	}
}

func cnpgActivityFixture(t *testing.T, ns string) {
	t.Helper()
	store := useMemoryTimeline(t)
	attributed := map[string]string{pkgtimeline.CNPGClusterLabel: "pg-orders"}
	clusterOwner := &timeline.OwnerInfo{Kind: "Cluster", Name: "pg-orders", APIVersion: "postgresql.cnpg.io/v1", UID: "orders-uid"}
	seedActivity(t, store, ns,
		activityRow{id: "cluster-update", apiVersion: "postgresql.cnpg.io/v1", kind: "Cluster", name: "pg-orders", uid: "orders-uid", age: time.Hour},
		activityRow{id: "backup-add", apiVersion: "postgresql.cnpg.io/v1", kind: "Backup", name: "pg-orders-b1", uid: "b1", eventType: timeline.EventTypeAdd, age: 50 * time.Minute, labels: attributed},
		activityRow{id: "backup-delete", apiVersion: "postgresql.cnpg.io/v1", kind: "Backup", name: "pg-orders-b1", uid: "b1", eventType: timeline.EventTypeDelete, age: 40 * time.Minute, labels: attributed},
		activityRow{id: "backup-k8s-event", apiVersion: "postgresql.cnpg.io/v1", kind: "Backup", name: "pg-orders-b1", uid: "b1", source: timeline.SourceK8sEvent, eventType: timeline.EventTypeWarning, age: 45 * time.Minute},
		activityRow{id: "pod-update", apiVersion: "v1", kind: "Pod", name: "pg-orders-1", uid: "p1", age: 30 * time.Minute, labels: attributed, owner: clusterOwner},
		activityRow{id: "pod-k8s-event", apiVersion: "v1", kind: "Pod", name: "pg-orders-1", uid: "p1", source: timeline.SourceK8sEvent, eventType: timeline.EventTypeWarning, age: 20 * time.Minute},
		activityRow{id: "old-pooler", apiVersion: "postgresql.cnpg.io/v1", kind: "Pooler", name: "pg-orders-rw", uid: "pool1", age: 48 * time.Hour, labels: attributed},
		activityRow{id: "other-backup", apiVersion: "postgresql.cnpg.io/v1", kind: "Backup", name: "pg-other-b1", uid: "b2", age: 10 * time.Minute, labels: map[string]string{pkgtimeline.CNPGClusterLabel: "pg-other"}},
		activityRow{id: "velero-backup", apiVersion: "velero.io/v1", kind: "Backup", name: "pg-orders", uid: "v1", age: 10 * time.Minute, labels: attributed},
		activityRow{id: "capi-cluster", apiVersion: "cluster.x-k8s.io/v1beta1", kind: "Cluster", name: "pg-orders", uid: "capi", age: 10 * time.Minute},
		activityRow{id: "impostor-pod", apiVersion: "v1", kind: "Pod", name: "impostor", uid: "p9", age: 10 * time.Minute, labels: attributed},
	)
	seedActivity(t, store, "elsewhere",
		activityRow{id: "elsewhere-backup", apiVersion: "postgresql.cnpg.io/v1", kind: "Backup", name: "pg-orders-b9", uid: "b9", age: 10 * time.Minute, labels: attributed},
	)
}

func decodeActivity(t *testing.T, resp *http.Response) CNPGClusterActivityResponse {
	t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var out CNPGClusterActivityResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func activityIDs(resp CNPGClusterActivityResponse) []string {
	var out []string
	for _, e := range resp.Events {
		out = append(out, e.ID)
	}
	return out
}

func TestCNPGClusterActivity_AttributesDeletedChildren(t *testing.T) {
	cnpgActivityFixture(t, "pgact")
	resp, err := http.Get(testServer.URL + "/api/cnpg/clusters/pgact/pg-orders/activity")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeActivity(t, resp)
	want := "pod-k8s-event,pod-update,backup-delete,backup-k8s-event,backup-add,cluster-update"
	if strings.Join(activityIDs(got), ",") != want {
		t.Fatalf("events = %v, want %s", activityIDs(got), want)
	}
	if got.Truncated {
		t.Error("truncated on a small history")
	}
	if got.Oldest == nil || got.AttributionSince == nil {
		t.Fatalf("oldest=%v attributionSince=%v", got.Oldest, got.AttributionSince)
	}
	if age := time.Since(*got.AttributionSince); age < 47*time.Hour {
		t.Errorf("attributionSince = %v, want the 48h-old Pooler row outside the window", got.AttributionSince)
	}

	resp, _ = http.Get(testServer.URL + "/api/cnpg/clusters/pgact/pg-orders/activity?limit=2&since=" + time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339))
	got = decodeActivity(t, resp)
	if len(got.Events) != 2 || !got.Truncated || got.Events[0].ID != "pod-k8s-event" {
		t.Fatalf("limited: events=%v truncated=%v", activityIDs(got), got.Truncated)
	}

	for _, q := range []string{"?since=yesterday", "?limit=0", "?limit=x"} {
		resp, _ := http.Get(testServer.URL + "/api/cnpg/clusters/pgact/pg-orders/activity" + q)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, resp.StatusCode)
		}
	}
}

func TestCNPGClusterActivity_DropsKindsTheCallerCannotList(t *testing.T) {
	cnpgActivityFixture(t, "pgactauth")
	env := newAuthTestServer(t)
	for _, u := range []struct {
		name          string
		clusterGet    bool
		backupsListed bool
		eventsListed  bool
	}{{"reader", true, true, true}, {"no-backups", true, false, true}, {"no-clusters", false, true, true}, {"no-events", true, true, false}} {
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"pgactauth"}}
		perms.SetCanI("get", cnpgGroup, "clusters", "pgactauth", u.clusterGet)
		allow(perms, "", "events", "pgactauth", u.eventsListed)
		allow(perms, cnpgGroup, "clusters", "pgactauth", true)
		allow(perms, cnpgGroup, "backups", "pgactauth", u.backupsListed)
		allow(perms, cnpgGroup, "poolers", "pgactauth", true)
		allow(perms, "", "pods", "pgactauth", true)
		env.srv.permCache.Set(u.name, nil, perms)
	}

	control := decodeActivity(t, env.authGet(t, "/api/cnpg/clusters/pgactauth/pg-orders/activity", "reader", ""))
	if !strings.Contains(strings.Join(activityIDs(control), ","), "backup-delete") {
		t.Fatalf("control: deleted Backup missing: %v", activityIDs(control))
	}

	got := decodeActivity(t, env.authGet(t, "/api/cnpg/clusters/pgactauth/pg-orders/activity", "no-backups", ""))
	for _, e := range got.Events {
		if e.Kind == "Backup" {
			t.Errorf("Backup row reached a caller who cannot list backups: %s", e.ID)
		}
	}
	if len(got.Events) != 3 {
		t.Errorf("events = %v, want the Cluster and Pod rows", activityIDs(got))
	}

	sawEvent := false
	for _, e := range control.Events {
		if e.Source == pkgtimeline.SourceK8sEvent {
			sawEvent = true
		}
	}
	if !sawEvent {
		t.Fatalf("control: no Kubernetes Event rows: %v", activityIDs(control))
	}
	noEvents := decodeActivity(t, env.authGet(t, "/api/cnpg/clusters/pgactauth/pg-orders/activity", "no-events", ""))
	for _, e := range noEvents.Events {
		if e.Source == pkgtimeline.SourceK8sEvent {
			t.Errorf("Kubernetes Event row reached a caller who cannot list events: %s", e.ID)
		}
	}

	resp := env.authGet(t, "/api/cnpg/clusters/pgactauth/pg-orders/activity", "no-clusters", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("no cluster get: status = %d, want 403", resp.StatusCode)
	}
}

func TestCNPGClusterLogsStream_SendsParsedInstanceLines(t *testing.T) {
	seedCNPGLogCluster(t, "pgstream")
	useLogServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", testServer.URL+"/api/cnpg/clusters/pgstream/pg-orders/logs/stream?pod=pg-orders-2", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 1024)
	for !strings.Contains(string(buf), "event: log") {
		n, err := resp.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			t.Fatalf("stream ended before a log event: %v\n%s", err, buf)
		}
	}
	stream := string(buf)
	sawConnected := strings.Contains(stream, "event: connected") && strings.Contains(stream, `"name":"pg-orders-2"`)
	if !sawConnected || strings.Contains(stream, `"name":"pg-orders-1"`) {
		t.Fatalf("connected event wrong:\n%s", stream)
	}
	for _, want := range []string{`"level":"LOG"`, `"message":"hello from pg-orders-2"`, `"sourceLabel":"replica 2"`} {
		if !strings.Contains(stream, want) {
			t.Errorf("stream missing %s:\n%s", want, stream)
		}
	}
}

func TestCNPGClusterActivity_RecreatedClusterExcludesPreviousIncarnationPods(t *testing.T) {
	owner := func(uid string) *timeline.OwnerInfo {
		return &timeline.OwnerInfo{Kind: "Cluster", Name: "pg-orders", APIVersion: "postgresql.cnpg.io/v1", UID: uid}
	}
	// Seeding the cache records the Cluster's own rows, so the Pod rows are
	// seeded into a fresh store afterwards and only Pod rows are compared.
	podHistory := func(live ...runtime.Object) []string {
		k8s.ResetTestDynamicState()
		seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, live...)
		store := useMemoryTimeline(t)
		seedActivity(t, store, "pgrecreate",
			activityRow{id: "current-pod", apiVersion: "v1", kind: "Pod", name: "pg-orders-1", uid: "p-new", age: 10 * time.Minute, owner: owner("orders-uid")},
			activityRow{id: "old-pod", apiVersion: "v1", kind: "Pod", name: "pg-orders-1", uid: "p-old", age: 2 * time.Hour, owner: owner("previous-uid")},
			activityRow{id: "old-pod-event", apiVersion: "v1", kind: "Pod", name: "pg-orders-1", uid: "p-old", source: timeline.SourceK8sEvent, eventType: timeline.EventTypeWarning, age: 90 * time.Minute},
		)
		resp, err := http.Get(testServer.URL + "/api/cnpg/clusters/pgrecreate/pg-orders/activity")
		if err != nil {
			t.Fatal(err)
		}
		var pods []string
		for _, e := range decodeActivity(t, resp).Events {
			if e.Kind == "Pod" {
				pods = append(pods, e.ID)
			}
		}
		return pods
	}

	live := withUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pgrecreate", "pg-orders", nil, nil), "orders-uid")
	if got := strings.Join(podHistory(live), ","); got != "current-pod" {
		t.Fatalf("with the Cluster live: pod events = %s, want only the current incarnation's", got)
	}
	if got := strings.Join(podHistory(), ","); got != "current-pod,old-pod-event,old-pod" {
		t.Fatalf("with the Cluster deleted: pod events = %s, want every incarnation", got)
	}
}

func TestCNPGStreamCursorResumesWithoutReplay(t *testing.T) {
	var c cnpgStreamCursor
	first := c.restartOptions("postgres", 200, nil)
	if first.TailLines == nil || *first.TailLines != 200 || first.SinceTime != nil || !first.Follow || !first.Timestamps || first.Container != "postgres" {
		t.Fatalf("first start = %+v", first)
	}
	line := func(ts, content string) workloadLogEntry {
		return workloadLogEntry{Timestamp: ts, Content: content}
	}
	for _, e := range []workloadLogEntry{line("2026-09-28T14:00:00.1Z", "a"), line("2026-09-28T14:00:05.7Z", "b"), line("2026-09-28T14:00:05.7Z", "c")} {
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
	replayed := []workloadLogEntry{line("2026-09-28T14:00:05.2Z", "earlier in the second"), line("2026-09-28T14:00:05.7Z", "b"), line("2026-09-28T14:00:05.7Z", "c")}
	for _, e := range replayed {
		if c.admit(e) {
			t.Errorf("replayed line %+v admitted", e)
		}
	}
	for _, e := range []workloadLogEntry{line("2026-09-28T14:00:05.7Z", "d"), line("2026-09-28T14:00:06Z", "e")} {
		if !c.admit(e) {
			t.Errorf("new line %+v rejected", e)
		}
	}
	if c.admit(line("2026-09-28T14:00:05.7Z", "d")) {
		t.Error("line before the new last timestamp admitted")
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

func TestCNPGIntervalLogSources(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 9, 29, 23, m, 0, 0, time.UTC) }
	since, until := at(20), at(30)
	pod := func(status corev1.ContainerStatus) *corev1.Pod {
		p := cnpgPod("ns", "pg-1", "pg")
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

// An incident followed by restarts: the interval lives in the previous run,
// and the current run's lines (all after the interval) fill the byte cap.
func TestCNPGClusterLogs_IntervalReadsThePreviousRun(t *testing.T) {
	ns := "pglogiv"
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		withUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", ns, "pg-orders", map[string]any{"instances": int64(1)}, nil), "iv-uid"),
	)
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: "iv-uid", Controller: boolPtr(true)}
	at := func(m int) time.Time { return time.Date(2026, 9, 29, 23, m, 0, 0, time.UTC) }
	p := cnpgPod(ns, "pg-orders-1", "pg-orders", owner)
	p.Status.ContainerStatuses = []corev1.ContainerStatus{cnpgRestartedStatus(at(45), at(10), at(40), 1)}
	seedCNPGPods(t, p)

	apiserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("previous") == "true" {
			fmt.Fprintln(w, "2026-09-29T23:25:00.5Z {\"level\":\"error\",\"msg\":\"during the incident\"}")
			return
		}
		for i := 0; i < 2000; i++ {
			fmt.Fprintf(w, "2026-09-29T23:50:%02d.5Z {\"level\":\"info\",\"msg\":\"after the restart %d\"}\n", i%60, i)
		}
	}))
	t.Cleanup(apiserver.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: apiserver.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })

	status, got, body := getCNPGLogs(t, "/api/cnpg/clusters/"+ns+"/pg-orders/logs?sinceTime=2026-09-29T23:20:00Z&untilTime=2026-09-29T23:30:00Z")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if len(got.Logs) != 1 || got.Logs[0].Message != "during the incident" || !got.Logs[0].Previous || got.Logs[0].SourceLabel != "primary 1 · previous run" {
		t.Fatalf("logs = %+v, want the previous run's interval line", got.Logs)
	}
	if strings.Contains(got.Notice, "snapshot limit") {
		t.Errorf("notice = %q: the clip came after the interval and cut nothing from it", got.Notice)
	}
	if !strings.Contains(got.EmptyMessage, "interval") {
		t.Errorf("emptyMessage = %q, want interval-specific copy", got.EmptyMessage)
	}
}
