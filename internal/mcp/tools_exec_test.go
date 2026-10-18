package mcp

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/skyhook-io/radar/internal/k8s"
)

func enableExecToolsForTest(t *testing.T) {
	t.Helper()
	prev := execToolsEnabled.Load()
	SetExecToolsEnabled(true)
	t.Cleanup(func() { SetExecToolsEnabled(prev) })
}

func TestExecToolsAreOptIn(t *testing.T) {
	names := func(includeWrites bool) []string {
		var out []string
		for _, tool := range listRegisteredToolsWith(t, includeWrites) {
			out = append(out, tool.Name)
		}
		return out
	}
	if got := names(true); slices.Contains(got, "exec_pod") {
		t.Fatal("exec_pod registered without --mcp-exec-tools")
	}
	enableExecToolsForTest(t)
	if got := names(false); slices.Contains(got, "exec_pod") {
		t.Fatal("exec_pod registered on the read-only server")
	}
	var found bool
	for _, tool := range listRegisteredToolsWith(t, true) {
		if tool.Name != "exec_pod" {
			continue
		}
		found = true
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Fatalf("exec_pod must be annotated destructive: %+v", tool.Annotations)
		}
	}
	if !found {
		t.Fatal("exec_pod not registered with exec tools enabled")
	}
}

func runningPod(name string, ready bool, containers ...string) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
	for _, c := range containers {
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: c})
	}
	return pod
}

type execCall struct {
	pod, container string
	command        []string
}

func fakeExec(calls *[]execCall, stdout, stderr string, err error) execStreams {
	return func(ctx context.Context, namespace, pod, container string, command []string, out, errOut io.Writer) error {
		*calls = append(*calls, execCall{pod: pod, container: container, command: command})
		_, _ = io.WriteString(out, stdout)
		_, _ = io.WriteString(errOut, stderr)
		return err
	}
}

func TestExecPodReturnsOutputAndExitCode(t *testing.T) {
	pod := runningPod("web-1", true, "istio-proxy", "app")
	pod.Annotations = map[string]string{"kubectl.kubernetes.io/default-container": "app"}
	client := fake.NewClientset(pod)
	var calls []execCall

	res, _, err := execPod(t.Context(), client, fakeExec(&calls, "200\n", "warn\n", nil), execPodInput{
		Namespace: "shop", Name: "web-1", Command: []string{"sh", "-c", "wget -qO- http://api:8080/healthz"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["exitCode"] != float64(0) || got["stdout"] != "200\n" || got["stderr"] != "warn\n" || got["container"] != "app" {
		t.Fatalf("result: %+v", got)
	}
	if len(calls) != 1 || calls[0].container != "app" || calls[0].command[2] != "wget -qO- http://api:8080/healthz" {
		t.Fatalf("exec call: %+v", calls)
	}

	res, _, err = execPod(t.Context(), client, fakeExec(&calls, "", "psql: connection refused\n", k8sexec.CodeExitError{Err: errors.New("command terminated with exit code 2"), Code: 2}), execPodInput{
		Namespace: "shop", Name: "web-1", Container: "app", Command: []string{"psql", "-c", "select 1"},
	})
	if err != nil {
		t.Fatalf("a non-zero exit must be a result, not a tool error: %v", err)
	}
	if got := decodeToolResult(t, res); got["exitCode"] != float64(2) || !strings.Contains(got["stderr"].(string), "connection refused") {
		t.Fatalf("non-zero exit: %+v", got)
	}
}

func TestExecPodCapsOutput(t *testing.T) {
	client := fake.NewClientset(runningPod("web-1", true, "app"))
	var calls []execCall
	big := strings.Repeat("x", execOutputCap+100)
	res, _, err := execPod(t.Context(), client, fakeExec(&calls, big, "", nil), execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"cat", "/big"}})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if len(got["stdout"].(string)) != execOutputCap || got["stdoutTruncated"] == nil {
		t.Fatalf("stdout not capped: len=%d truncated=%v", len(got["stdout"].(string)), got["stdoutTruncated"])
	}
	if _, present := got["stderrTruncated"]; present {
		t.Fatal("empty stderr reported as truncated")
	}
}

func TestExecPodTimeoutReturnsPartialOutput(t *testing.T) {
	client := fake.NewClientset(runningPod("web-1", true, "app"))
	run := func(ctx context.Context, namespace, pod, container string, command []string, out, errOut io.Writer) error {
		_, _ = io.WriteString(out, "partial")
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	res, _, err := execPod(t.Context(), client, run, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"sleep", "100"}, TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not enforced")
	}
	got := decodeToolResult(t, res)
	if got["timedOut"] != true || got["stdout"] != "partial" || got["exitCode"] != nil {
		t.Fatalf("timeout result: %+v", got)
	}
}

func TestExecPodRefusesBadInput(t *testing.T) {
	pending := runningPod("starting", false, "app")
	pending.Status.Phase = corev1.PodPending
	client := fake.NewClientset(runningPod("web-1", true, "app"), pending)
	var calls []execCall
	run := fakeExec(&calls, "", "", nil)
	for name, tc := range map[string]struct {
		input execPodInput
		want  string
	}{
		"no command":        {execPodInput{Namespace: "shop", Name: "web-1"}, "command is required"},
		"timeout above max": {execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"ls"}, TimeoutSeconds: 301}, "at most 300"},
		"unknown container": {execPodInput{Namespace: "shop", Name: "web-1", Container: "db", Command: []string{"ls"}}, `container "db" not found`},
		"pod not running":   {execPodInput{Namespace: "shop", Name: "starting", Command: []string{"ls"}}, "not Running"},
		"missing pod":       {execPodInput{Namespace: "shop", Name: "ghost", Command: []string{"ls"}}, "failed to read pod"},
		"unknown kind":      {execPodInput{Namespace: "shop", Name: "web", Kind: "service", Command: []string{"ls"}}, "invalid kind"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := execPod(t.Context(), client, run, tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if len(calls) != 0 {
		t.Fatalf("refused input still ran: %+v", calls)
	}
}

func TestExecPodStreamFailureIsAToolError(t *testing.T) {
	client := fake.NewClientset(runningPod("web-1", true, "app"))
	var calls []execCall
	_, _, err := execPod(t.Context(), client, fakeExec(&calls, "", "", errors.New(`pods "web-1" is forbidden: cannot create resource "pods/exec"`)), execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"ls"}})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("RBAC denial must surface as a tool error: %v", err)
	}
}

func TestExecPodPicksReadyWorkloadPod(t *testing.T) {
	defer k8s.ResetTestState()
	controller := true
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "dep"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", UID: "rs", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", APIVersion: "apps/v1", Name: "web", UID: "dep", Controller: &controller}}}}
	owned := func(p *corev1.Pod, created time.Time) *corev1.Pod {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: "web-1", UID: "rs", Controller: &controller}}
		p.CreationTimestamp = metav1.NewTime(created)
		return p
	}
	now := time.Now()
	notReady := owned(runningPod("web-1-new", false, "app"), now)
	ready := owned(runningPod("web-1-old", true, "app"), now.Add(-time.Hour))
	if err := k8s.InitTestResourceCache(fake.NewClientset(dep, rs, notReady, ready)); err != nil {
		t.Fatal(err)
	}
	var calls []execCall
	res, _, err := execPod(t.Context(), fake.NewClientset(), fakeExec(&calls, "ok", "", nil), execPodInput{Namespace: "shop", Name: "web", Kind: "deploy", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["pod"] != "web-1-old" || !strings.Contains(got["podSelection"].(string), "of 2 pods") {
		t.Fatalf("picked %+v", got)
	}
}

func TestPickExecPod(t *testing.T) {
	terminating := runningPod("going", true, "app")
	terminating.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	pending := runningPod("pending", false, "app")
	pending.Status.Phase = corev1.PodPending
	if got := pickExecPod([]*corev1.Pod{terminating, pending}); got != nil {
		t.Fatalf("picked %s from no running pods", got.Name)
	}
	running := runningPod("running", false, "app")
	if got := pickExecPod([]*corev1.Pod{terminating, pending, running}); got == nil || got.Name != "running" {
		t.Fatalf("picked %v", got)
	}
}
