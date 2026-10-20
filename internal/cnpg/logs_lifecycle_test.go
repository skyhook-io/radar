package cnpg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/podlogs"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type cnpgLogEvent struct {
	kind string
	data any
}

func cnpgFollowTarget(t *testing.T, client kubernetes.Interface, pod *corev1.Pod) (*LogTarget, *k8sfake.Clientset) {
	t.Helper()
	cluster := cnpgActionCluster(nil)
	inventory := k8sfake.NewSimpleClientset(pod)
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: inventory, ResourceTypes: map[string]bool{"pods": true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	reader := &Reader{Clients: ReadClients{Typed: client}, Observations: Observations{
		DynamicList: func(context.Context, *k8s.ResourceCache, string, string, string) ([]*unstructured.Unstructured, error) {
			return []*unstructured.Unstructured{cluster}, nil
		},
	}}
	return &LogTarget{reader: reader, namespace: cluster.GetNamespace(), name: cluster.GetName(), cluster: cluster, cache: &k8s.ResourceCache{ResourceCache: core}, pods: []*corev1.Pod{pod}}, inventory
}

func cnpgFollowPod(uid string) *corev1.Pod {
	pod := cnpgActionPod("pg-1", uid, true)
	pod.Labels["cnpg.io/instanceRole"] = "primary"
	pod.Spec.Containers = []corev1.Container{{Name: "postgres"}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "postgres", ContainerID: "containerd://retained", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	return pod
}

func cnpgFollowEvents(t *testing.T, target *LogTarget, query LogQuery, beforeSend func(string, any)) (<-chan cnpgLogEvent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	events := make(chan cnpgLogEvent, 2048)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		target.Follow(ctx, query, func(kind string, data any) {
			if beforeSend != nil {
				beforeSend(kind, data)
			}
			select {
			case events <- cnpgLogEvent{kind, data}:
			case <-ctx.Done():
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("CNPG log follow did not stop after cancellation")
		}
	})
	return events, cancel
}

func waitCNPGLogEvent(t *testing.T, events <-chan cnpgLogEvent, want func(cnpgLogEvent) bool) cnpgLogEvent {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("CNPG log follow ended before the expected event")
			}
			if want(event) {
				return event
			}
		case <-timer.C:
			t.Fatal("CNPG log follow did not emit the expected event")
		}
	}
}

func TestCNPGLogsFollowReplacesActivePodUID(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	oldCanceled, newCanceled := make(chan struct{}), make(chan struct{})
	roleChanged := make(chan struct{})
	requests := make(chan url.Values, 4)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ordinal := opens.Add(1)
		requests <- r.URL.Query()
		if ordinal == 1 {
			fmt.Fprintln(w, "2026-10-13T10:00:05Z old")
		} else {
			fmt.Fprintln(w, "2026-10-13T10:00:00Z new")
		}
		w.(http.Flusher).Flush()
		if ordinal == 2 {
			select {
			case <-roleChanged:
				fmt.Fprintln(w, "2026-10-13T10:00:01Z new role")
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
			}
		}
		<-r.Context().Done()
		if ordinal == 1 {
			close(oldCanceled)
		} else if ordinal == 2 {
			close(newCanceled)
		}
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	pod := cnpgFollowPod("old")
	target, inventory := cnpgFollowTarget(t, client, pod)
	events, cancel := cnpgFollowEvents(t, target, LogQuery{Container: "postgres", TailLines: 17}, nil)
	oldLine := func(event cnpgLogEvent) bool {
		return event.kind == "log" && event.data.(podlogs.Entry).Content == "old"
	}
	waitCNPGLogEvent(t, events, oldLine)
	<-requests
	replacement := pod.DeepCopy()
	replacement.UID = "new"
	replacement.Labels["cnpg.io/instanceRole"] = "replica"
	if _, err := inventory.CoreV1().Pods(pod.Namespace).Update(context.Background(), replacement, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitCNPGLogEvent(t, events, func(event cnpgLogEvent) bool {
		return event.kind == "pod_removed" && event.data.(map[string]string)["reason"] == "replaced"
	})
	newLine := waitCNPGLogEvent(t, events, func(event cnpgLogEvent) bool {
		return event.kind == "log" && event.data.(podlogs.Entry).Content == "new"
	})
	if label := newLine.data.(podlogs.Entry).SourceLabel; label != "replica 1" {
		t.Fatalf("replacement did not adopt its own role: %q", label)
	}
	select {
	case <-oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("same-name replacement did not cancel the old Pod's stream")
	}
	select {
	case options := <-requests:
		if options.Get("sinceTime") != "" || options.Get("tailLines") != "17" {
			t.Fatalf("replacement reused the old Pod's cursor: %v", options)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement did not open a fresh stream")
	}
	replacement = replacement.DeepCopy()
	replacement.Labels["cnpg.io/instanceRole"] = "primary"
	if _, err := inventory.CoreV1().Pods(pod.Namespace).Update(context.Background(), replacement, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(logDiscoveryInterval + 250*time.Millisecond)
	if count := opens.Load(); count != 2 {
		t.Fatalf("old worker cleanup removed the replacement's active stream: %d opens", count)
	}
	close(roleChanged)
	updatedLine := waitCNPGLogEvent(t, events, func(event cnpgLogEvent) bool {
		return event.kind == "log" && event.data.(podlogs.Entry).Content == "new role"
	})
	if label := updatedLine.data.(podlogs.Entry).SourceLabel; label != "primary 1" {
		t.Fatalf("current Pod's live role update was lost: %q", label)
	}
	cancel()
	select {
	case <-newCanceled:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not close the replacement's stream")
	}
}

func TestCNPGLogsFollowEndsWhenCapturedClusterChanges(t *testing.T) {
	t.Parallel()
	childCanceled := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "2026-10-13T10:00:00Z old cluster")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(childCanceled)
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := cnpgFollowTarget(t, client, cnpgFollowPod("old-cluster-pod"))
	clusterDone := make(chan struct{})
	target.reader.Clients.ClusterDone = clusterDone
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	events := make(chan cnpgLogEvent, 32)
	go func() {
		defer close(events)
		target.Follow(parent, LogQuery{Container: "postgres", TailLines: 17}, func(kind string, data any) {
			events <- cnpgLogEvent{kind, data}
		})
	}()
	waitCNPGLogEvent(t, events, func(event cnpgLogEvent) bool { return event.kind == "log" })
	close(clusterDone)
	end := waitCNPGLogEvent(t, events, func(event cnpgLogEvent) bool { return event.kind == "end" })
	if reason := end.data.(map[string]string)["reason"]; reason != "context changed" || parent.Err() != nil {
		t.Fatalf("cluster switch did not end the follow independently of its parent: reason=%q parentErr=%v", reason, parent.Err())
	}
	select {
	case <-childCanceled:
	case <-time.After(time.Second):
		t.Fatal("cluster switch did not cancel its captured client's child stream")
	}
	select {
	case event, ok := <-events:
		if ok {
			t.Fatalf("event from the old cluster appeared after end: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("follow remained active after the cluster switch")
	}
}

type cnpgLogTransport func(*http.Request) (*http.Response, error)

func (transport cnpgLogTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestCNPGLogsFollowCompletedPreviousRunIsBoundToPodUID(t *testing.T) {
	t.Parallel()
	requests := make(chan *http.Request, 4)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://logs.test", Transport: cnpgLogTransport(func(request *http.Request) (*http.Response, error) {
		requests <- request
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("2026-10-13T10:00:00Z retained final line"))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	pod := cnpgFollowPod("old")
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ContainerID: "containerd://retained"}
	target, inventory := cnpgFollowTarget(t, client, pod)
	discovery := make(chan struct{}, 4)
	target.reader.Observations.DynamicList = func(context.Context, *k8s.ResourceCache, string, string, string) ([]*unstructured.Unstructured, error) {
		discovery <- struct{}{}
		return []*unstructured.Unstructured{target.cluster}, nil
	}
	events, _ := cnpgFollowEvents(t, target, LogQuery{Container: "postgres", TailLines: 17}, nil)
	read := func() *http.Request {
		t.Helper()
		event := waitCNPGLogEvent(t, events, func(event cnpgLogEvent) bool { return event.kind == "log" })
		entry := event.data.(podlogs.Entry)
		if entry.Content != "retained final line" || !entry.Previous || !strings.HasSuffix(entry.SourceLabel, "previous run") {
			t.Fatalf("retained final EOF line lost its previous-run identity: %+v", entry)
		}
		request := <-requests
		if request.URL.Query().Get("previous") != "true" || request.URL.Query().Get("follow") == "true" {
			t.Fatalf("waiting container did not read its retained run without following: %s", request.URL.RawQuery)
		}
		select {
		case <-request.Context().Done():
		case <-time.After(time.Second):
			t.Fatal("finished child stream retained its request context")
		}
		return request
	}
	read()
	select {
	case <-discovery:
	case <-time.After(8 * time.Second):
		t.Fatal("no instance rediscovery")
	}
	select {
	case <-requests:
		t.Fatal("the same completed previous run was reopened")
	case <-time.After(50 * time.Millisecond):
	}
	replacement := pod.DeepCopy()
	replacement.UID = "new"
	if _, err := inventory.CoreV1().Pods(pod.Namespace).Update(context.Background(), replacement, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	read()
}

func TestCNPGLogsFollowIncompleteCompletedRunIsRetried(t *testing.T) {
	t.Parallel()
	requests := make(chan *http.Request, 4)
	var opens atomic.Int32
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://logs.test", Transport: cnpgLogTransport(func(request *http.Request) (*http.Response, error) {
		requests <- request
		var body io.Reader = strings.NewReader("2026-10-13T10:00:00Z complete final line")
		if opens.Add(1) == 1 {
			body = io.MultiReader(strings.NewReader("2026-10-13T10:00:00Z interrupted fragment"), iotest.ErrReader(io.ErrUnexpectedEOF))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	pod := cnpgFollowPod("finished")
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ContainerID: "containerd://retained", ExitCode: 0}}
	target, _ := cnpgFollowTarget(t, client, pod)
	events, _ := cnpgFollowEvents(t, target, LogQuery{Container: "postgres", TailLines: 17}, nil)
	first := <-requests
	select {
	case <-first.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("interrupted child stream retained its request context")
	}
	event := waitCNPGLogEvent(t, events, func(event cnpgLogEvent) bool { return event.kind == "log" })
	if entry := event.data.(podlogs.Entry); entry.Content != "complete final line" || entry.Previous {
		t.Fatalf("incomplete read was emitted or incorrectly certified as completed: %+v", entry)
	}
	if opens.Load() != 2 {
		t.Fatalf("incomplete completed-container read was not retried: opens=%d", opens.Load())
	}
}

type cnpgQueuedLogBody struct {
	ctx     context.Context
	produce <-chan struct{}
	queued  chan<- struct{}
	next    int
}

func (body *cnpgQueuedLogBody) Read(buffer []byte) (int, error) {
	if body.next == 0 {
		body.next++
		return copy(buffer, "2026-10-13T10:00:05Z initial\n"), nil
	}
	if body.next == 1 {
		select {
		case <-body.produce:
		case <-body.ctx.Done():
			return 0, body.ctx.Err()
		}
	}
	if body.next <= 512 {
		line := fmt.Sprintf("2026-10-13T10:00:06Z queued %d\n", body.next)
		body.next++
		return copy(buffer, line), nil
	}
	if body.next == 513 {
		body.next++
		close(body.queued)
	}
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (*cnpgQueuedLogBody) Close() error { return nil }

func TestCNPGLogsFollowQueuedOldLinesDoNotClearReplacementNotice(t *testing.T) {
	t.Parallel()
	produce, queued, newRequest := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var opens atomic.Int32
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://logs.test", Transport: cnpgLogTransport(func(request *http.Request) (*http.Response, error) {
		var body io.ReadCloser
		ordinal := opens.Add(1)
		if ordinal == 1 {
			body = &cnpgQueuedLogBody{ctx: request.Context(), produce: produce, queued: queued}
		} else {
			if ordinal == 2 {
				close(newRequest)
			}
			body = io.NopCloser(strings.NewReader("unable to retrieve container logs for containerd://replacement"))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	pod := cnpgFollowPod("old")
	target, inventory := cnpgFollowTarget(t, client, pod)
	var discovering atomic.Bool
	target.reader.Observations.DynamicList = func(ctx context.Context, _ *k8s.ResourceCache, _, _, _ string) ([]*unstructured.Unstructured, error) {
		if discovering.CompareAndSwap(false, true) {
			close(produce)
			select {
			case <-queued:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []*unstructured.Unstructured{target.cluster}, nil
	}
	events, cancel := cnpgFollowEvents(t, target, LogQuery{Container: "postgres", TailLines: 17}, func(kind string, data any) {
		if kind == "log" && strings.HasPrefix(data.(podlogs.Entry).Content, "queued ") {
			select {
			case <-newRequest:
			case <-time.After(time.Second):
			}
			runtime.Gosched()
		}
	})
	waitCNPGLogEvent(t, events, func(event cnpgLogEvent) bool { return event.kind == "log" })
	replacement := pod.DeepCopy()
	replacement.UID = "new"
	replacement.Labels["cnpg.io/instanceRole"] = "replica"
	if _, err := inventory.CoreV1().Pods(pod.Namespace).Update(context.Background(), replacement, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	seenNotice, oldAfterNotice := false, false
	last := waitCNPGLogEvent(t, events, func(event cnpgLogEvent) bool {
		if event.kind == "notice" {
			if event.data.(map[string]string)["notice"] == "" {
				t.Error("queued output from the old Pod cleared the unreadable replacement's notice")
			} else {
				seenNotice = true
			}
		}
		if event.kind == "log" && strings.HasPrefix(event.data.(podlogs.Entry).Content, "queued ") {
			if label := event.data.(podlogs.Entry).SourceLabel; label != "primary 1" {
				t.Fatalf("old primary's queued record inherited its replacement's role: %q", label)
			}
			oldAfterNotice = oldAfterNotice || seenNotice
			return event.data.(podlogs.Entry).Content == "queued 512"
		}
		return false
	})
	if !seenNotice || !oldAfterNotice || last.data.(podlogs.Entry).Content != "queued 512" {
		t.Fatalf("fixture did not retain queued old lines after the replacement failure: notice=%v oldAfterNotice=%v last=%+v", seenNotice, oldAfterNotice, last)
	}
	cancel()
}
