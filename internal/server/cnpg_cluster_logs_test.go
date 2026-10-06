package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestCNPGLogsAllContainers(t *testing.T) {
	ns := "pglogsall"
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, withUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", ns, "pg-orders", map[string]any{"instances": int64(1)}, nil), "orders-uid"))
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: "orders-uid", Controller: boolPtr(true)}
	pod := cnpgPod(ns, "pg-orders-1", "pg-orders", owner)
	pod.Spec.Containers = []corev1.Container{{Name: "postgres"}, {Name: "metrics"}}
	pod.Spec.InitContainers = []corev1.Container{{Name: "plugin-barman-cloud"}, {Name: "not-started"}}
	started := metav1.NewTime(time.Date(2026, 9, 28, 14, 0, 0, 0, time.UTC))
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "postgres", State: running}, {Name: "metrics", State: running}}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "plugin-barman-cloud", State: running}}
	seedCNPGPods(t, pod)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/log") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "2026-09-28T14:19:58Z hello %s\n", r.URL.Query().Get("container"))
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	for _, suffix := range []string{"?container=all", "?container=all&sinceTime=2026-09-28T14:00:00Z&untilTime=2026-09-28T14:30:00Z"} {
		status, got, body := getCNPGLogs(t, "/api/cnpg/clusters/"+ns+"/pg-orders/logs"+suffix)
		if status != http.StatusOK {
			t.Fatalf("%d: %s", status, body)
		}
		found := map[string]bool{}
		for _, line := range got.Logs {
			found[line.Container] = true
			if !strings.Contains(line.SourceLabel, line.Container) {
				t.Errorf("missing container label: %+v", line)
			}
		}
		for _, name := range []string{"postgres", "metrics", "plugin-barman-cloud"} {
			if !found[name] {
				t.Errorf("missing %s: %+v", name, got)
			}
		}
		if found["not-started"] {
			t.Error("read unstarted init container")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", testServer.URL+"/api/cnpg/clusters/"+ns+"/pg-orders/logs/stream?container=all&pod=pg-orders-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	stream := ""
	for scanner.Scan() {
		stream += scanner.Text() + "\n"
		if strings.Contains(stream, `"container":"postgres"`) && strings.Contains(stream, `"container":"metrics"`) && strings.Contains(stream, `"container":"plugin-barman-cloud"`) {
			break
		}
	}
	for _, container := range []string{"postgres", "metrics", "plugin-barman-cloud"} {
		if !strings.Contains(stream, `"sourceLabel":"primary 1 · `+container+`"`) {
			t.Errorf("stream did not label %s: %s", container, stream)
		}
	}
}

func TestCNPGLogsContainerSelection(t *testing.T) {
	q, err := parseCNPGLogQuery(httptest.NewRequest("GET", "/?container=all", nil), time.Now())
	if err != nil || q.container != "all" {
		t.Fatalf("%+v %v", q, err)
	}
	q, err = parseCNPGLogQuery(httptest.NewRequest("GET", "/", nil), time.Now())
	if err != nil || q.container != "postgres" {
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

func TestCNPGLogsStreamCompletedContainers(t *testing.T) {
	ns := "pglogscompleted"
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, withUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", ns, "pg-orders", nil, nil), "orders-uid"))
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: "orders-uid", Controller: boolPtr(true)}
	pod := cnpgPod(ns, "pg-orders-1", "pg-orders", owner)
	pod.Spec.InitContainers = []corev1.Container{{Name: "bootstrap-controller"}, {Name: "not-started"}}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "bootstrap-controller", ContainerID: "containerd://old", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
	seedCNPGPods(t, pod)
	var mu sync.Mutex
	counts := map[string]int{}
	requests := make(chan string, 100)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		container := r.URL.Query().Get("container")
		mu.Lock()
		counts[container]++
		mu.Unlock()
		requests <- container
		fmt.Fprintln(w, "2026-09-28T14:19:58Z line")
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", testServer.URL+"/api/cnpg/clusters/"+ns+"/pg-orders/logs/stream?container=all", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for runningReads := 0; runningReads < 3; {
		select {
		case container := <-requests:
			if container == "postgres" {
				runningReads++
			}
		case <-ctx.Done():
			t.Fatal("stream did not rediscover running containers")
		}
	}
	mu.Lock()
	finishedReads, unstartedReads := counts["bootstrap-controller"], counts["not-started"]
	mu.Unlock()
	if finishedReads != 1 || unstartedReads != 0 {
		t.Fatalf("finished reads=%d, unstarted reads=%d", finishedReads, unstartedReads)
	}
	pod.Status.InitContainerStatuses[0].ContainerID = "containerd://new"
	pod.Status.InitContainerStatuses[0].RestartCount++
	if _, err := testFakeClient.CoreV1().Pods(ns).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case container := <-requests:
			if container == "bootstrap-controller" {
				return
			}
		case <-ctx.Done():
			t.Fatal("a new terminated container run was not read")
		}
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

func TestCNPGLogsWaitingPreviousRun(t *testing.T) {
	ns := "pglogscrash"
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, withUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", ns, "pg-orders", nil, nil), "orders-uid"))
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: "orders-uid", Controller: boolPtr(true)}
	pod := cnpgPod(ns, "pg-orders-1", "pg-orders", owner)
	pod.Spec.Containers = []corev1.Container{{Name: "postgres"}, {Name: "metrics"}, {Name: "not-started"}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "postgres", RestartCount: 1, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ContainerID: "containerd://crash", ExitCode: 1}}},
		{Name: "metrics", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: "not-started", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
	}
	seedCNPGPods(t, pod)
	var mu sync.Mutex
	counts := map[string]int{}
	requests := make(chan string, 100)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		container := r.URL.Query().Get("container")
		if container == "postgres" && (r.URL.Query().Get("previous") != "true" || r.URL.Query().Get("follow") == "true") {
			t.Errorf("wrong crash-log options: %s", r.URL.RawQuery)
		}
		mu.Lock()
		counts[container]++
		mu.Unlock()
		requests <- container
		fmt.Fprintf(w, "2026-09-28T14:19:58Z retained %s\n", container)
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	status, got, body := getCNPGLogs(t, "/api/cnpg/clusters/"+ns+"/pg-orders/logs?container=all")
	if status != http.StatusOK {
		t.Fatalf("%d: %s", status, body)
	}
	crash := false
	for _, line := range got.Logs {
		if line.Container == "postgres" {
			crash = line.Previous && strings.Contains(line.Content, "retained postgres")
		}
		if line.Container == "not-started" {
			t.Fatal("read container that never started")
		}
	}
	if !crash {
		t.Fatalf("retained crash logs missing: %+v", got)
	}
	mu.Lock()
	counts = map[string]int{}
	mu.Unlock()
	for len(requests) > 0 {
		<-requests
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", testServer.URL+"/api/cnpg/clusters/"+ns+"/pg-orders/logs/stream?container=all", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	stream := ""
	for scanner.Scan() {
		stream += scanner.Text() + "\n"
		if strings.Contains(stream, `"container":"postgres"`) {
			break
		}
	}
	if !strings.Contains(stream, `"previous":true`) || !strings.Contains(stream, "retained postgres") || !strings.Contains(stream, `"sourceLabel":"primary 1 · postgres · previous run"`) {
		t.Fatalf("previous-run stream metadata missing: %s", stream)
	}
	for runningReads := 0; runningReads < 3; {
		select {
		case container := <-requests:
			if container == "metrics" {
				runningReads++
			}
		case <-ctx.Done():
			t.Fatal("stream did not rediscover running container")
		}
	}
	mu.Lock()
	crashReads, neverReads := counts["postgres"], counts["not-started"]
	mu.Unlock()
	if crashReads != 1 || neverReads != 0 {
		t.Fatalf("crash reads=%d, never-started reads=%d", crashReads, neverReads)
	}
}
