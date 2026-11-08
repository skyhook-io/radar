package mcp

import (
	"bytes"
	"context"
	"errors"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"io"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"log"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
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

	res, _, err := execPod(t.Context(), client, nil, fakeExec(&calls, "200\n", "warn\n", nil), execPodInput{
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

	res, _, err = execPod(t.Context(), client, nil, fakeExec(&calls, "", "psql: connection refused\n", k8sexec.CodeExitError{Err: errors.New("command terminated with exit code 2"), Code: 2}), execPodInput{
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
	res, _, err := execPod(t.Context(), client, nil, fakeExec(&calls, big, "", nil), execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"cat", "/big"}})
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
	res, _, err := execPod(t.Context(), client, nil, run, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"sleep", "100"}, TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not enforced")
	}
	got := decodeToolResult(t, res)
	if got["timedOut"] != true || got["stdout"] != "partial" || got["exitCode"] != nil || !strings.Contains(got["message"].(string), "Radar stopped waiting") {
		t.Fatalf("timeout result: %+v", got)
	}
}

func TestExecPodRefusesBadInput(t *testing.T) {
	succeeded := runningPod("succeeded", false, "app")
	succeeded.Status.Phase = corev1.PodSucceeded
	failed := runningPod("failed", false, "app")
	failed.Status.Phase = corev1.PodFailed
	terminating := runningPod("terminating", true, "app")
	terminating.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	client := fake.NewClientset(runningPod("web-1", true, "app"), succeeded, failed, terminating)
	var calls []execCall
	run := fakeExec(&calls, "", "", nil)
	for name, tc := range map[string]struct {
		input execPodInput
		want  string
	}{
		"terminating pod":   {execPodInput{Namespace: "shop", Name: "terminating", Command: []string{"ls"}}, "is terminating"},
		"no command":        {execPodInput{Namespace: "shop", Name: "web-1"}, "command is required"},
		"timeout above max": {execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"ls"}, TimeoutSeconds: 46}, "at most 45"},
		"unknown container": {execPodInput{Namespace: "shop", Name: "web-1", Container: "db", Command: []string{"ls"}}, `container "db" not found`},
		"succeeded pod":     {execPodInput{Namespace: "shop", Name: "succeeded", Command: []string{"ls"}}, "is Succeeded"},
		"failed pod":        {execPodInput{Namespace: "shop", Name: "failed", Command: []string{"ls"}}, "is Failed"},
		"missing pod":       {execPodInput{Namespace: "shop", Name: "ghost", Command: []string{"ls"}}, "failed to read pod"},
		"unknown kind":      {execPodInput{Namespace: "shop", Name: "web", Kind: "service", Command: []string{"ls"}}, "invalid kind"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := execPod(t.Context(), client, nil, run, tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if len(calls) != 0 {
		t.Fatalf("refused input still ran: %+v", calls)
	}
}

func TestExecPodNamedPendingSidecarReachesExecutor(t *testing.T) {
	pod := runningPod("starting", false, "app")
	pod.Status.Phase = corev1.PodPending
	restartAlways := corev1.ContainerRestartPolicyAlways
	pod.Spec.InitContainers = []corev1.Container{{Name: "sidecar", RestartPolicy: &restartAlways}}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name: "sidecar", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	client := fake.NewClientset(pod)
	for _, kind := range []string{"", "pod"} {
		t.Run("kind="+kind, func(t *testing.T) {
			var calls []execCall
			input := execPodInput{Namespace: "shop", Name: "starting", Kind: kind, Container: "sidecar", Command: []string{"true"}}
			res, _, err := execPod(t.Context(), client, nil, fakeExec(&calls, "ok", "", nil), input)
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || calls[0].pod != pod.Name || calls[0].container != "sidecar" {
				t.Fatalf("exec calls: %+v", calls)
			}
			if got := decodeToolResult(t, res); got["exitCode"] != float64(0) || got["stdout"] != "ok" {
				t.Fatalf("result: %+v", got)
			}

			containerErr := apierrors.NewBadRequest("container app is not running")
			input.Container = "app"
			_, _, err = execPod(t.Context(), client, nil, fakeExec(&calls, "", "", &execTransportError{error: containerErr, status: 400}), input)
			if !errors.Is(err, containerErr) || !strings.Contains(err.Error(), containerErr.Error()) {
				t.Fatalf("container state error must pass through: %v", err)
			}
			if len(calls) != 2 || calls[1].container != "app" {
				t.Fatalf("container state must be checked by the API server: %+v", calls)
			}
		})
	}
}

func TestExecPodStreamFailureIsAToolError(t *testing.T) {
	client := fake.NewClientset(runningPod("web-1", true, "app"))
	var calls []execCall
	_, _, err := execPod(t.Context(), client, nil, fakeExec(&calls, "", "", apierrors.NewForbidden(schema.GroupResource{Resource: "pods/exec"}, "web-1", errors.New("denied"))), execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"ls"}})
	if err == nil || !strings.Contains(err.Error(), "rbac_denied") || !strings.Contains(err.Error(), "get_subject_permissions") || !apierrors.IsForbidden(err) {
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
		p.Labels = map[string]string{"pod-template-hash": "new", "app": "web"}
		return p
	}
	rs.Annotations = map[string]string{"deployment.kubernetes.io/revision": "2"}
	rs.Labels = map[string]string{"pod-template-hash": "new", "app": "web"}
	now := time.Now()
	notReady := owned(runningPod("web-1-new", false, "app"), now)
	ready := owned(runningPod("web-1-old", true, "app"), now.Add(-time.Hour))
	if err := k8s.InitTestResourceCache(fake.NewClientset(dep, rs, notReady, ready)); err != nil {
		t.Fatal(err)
	}
	var calls []execCall
	res, _, err := execPod(t.Context(), fake.NewClientset(dep, rs, notReady, ready), nil, fakeExec(&calls, "ok", "", nil), execPodInput{Namespace: "shop", Name: "web", Kind: "deploy", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["pod"] != "web-1-old" || !strings.Contains(got["podSelection"].(string), "pods of desired revision: 2") {
		t.Fatalf("picked %+v", got)
	}
	changed := ready.DeepCopy()
	changed.Labels["pod-template-hash"] = "different"
	before := len(calls)
	freshClient := fake.NewClientset(dep, rs, notReady, ready)
	freshClient.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) { return true, changed, nil })
	_, _, err = execPod(t.Context(), freshClient, nil, fakeExec(&calls, "", "", nil), execPodInput{Namespace: "shop", Name: "web", Kind: "deploy", Command: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "changed") || len(calls) != before {
		t.Fatalf("stale selection executed: calls=%v error=%v", calls, err)
	}
}

func TestPickExecPod(t *testing.T) {
	terminating := runningPod("going", true, "app")
	terminating.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	pending := runningPod("pending", true, "app")
	pending.Status.Phase = corev1.PodPending
	if got := pickExecPod([]*corev1.Pod{terminating, pending}); got != nil {
		t.Fatalf("picked %s from no running pods", got.Name)
	}
	running := runningPod("running", false, "app")
	if got := pickExecPod([]*corev1.Pod{terminating, pending, running}); got != nil {
		t.Fatalf("picked %v", got)
	}
}

func TestExecPodSkipsMeshDefault(t *testing.T) {
	for _, proxy := range []string{"istio-proxy", "linkerd-proxy", "consul-dataplane"} {
		t.Run(proxy, func(t *testing.T) {
			client := fake.NewClientset(runningPod("web-1", true, proxy, "app"))
			var calls []execCall
			_, _, err := execPod(t.Context(), client, nil, fakeExec(&calls, "", "", nil), execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"true"}})
			if err != nil || len(calls) != 1 || calls[0].container != "app" {
				t.Fatalf("calls=%v err=%v", calls, err)
			}
		})
	}
}

func TestExecPodCurrentControllerRevision(t *testing.T) {
	controller := true
	owner := func(kind, name, uid, version string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: version, Kind: kind, Name: name, UID: types.UID(uid), Controller: &controller}}
	}
	for _, kind := range []string{"statefulsets", "daemonsets", "jobs", "replicasets"} {
		t.Run(kind, func(t *testing.T) {
			defer k8s.ResetTestState()
			old, current := runningPod("old", true, "app"), runningPod("current", true, "app")
			old.Labels = map[string]string{appsv1.ControllerRevisionHashLabelKey: "old"}
			current.Labels = map[string]string{appsv1.ControllerRevisionHashLabelKey: "latest"}
			old.CreationTimestamp = metav1.NewTime(time.Now())
			current.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
			meta := metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "root", Generation: 2}
			var objects []runtime.Object
			switch kind {
			case "statefulsets":
				objects = append(objects, &appsv1.StatefulSet{ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{}}, Status: appsv1.StatefulSetStatus{ObservedGeneration: 2, CurrentRevision: "old", UpdateRevision: "latest"}})
				old.OwnerReferences = owner("StatefulSet", "web", "root", "apps/v1")
			case "daemonsets":
				objects = append(objects, &appsv1.DaemonSet{ObjectMeta: meta, Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{}}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 2}})
				old.OwnerReferences = owner("DaemonSet", "web", "root", "apps/v1")
				for i, hash := range []string{"old", "latest"} {
					objects = append(objects, &appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: hash, Namespace: "shop", OwnerReferences: old.OwnerReferences, Labels: map[string]string{appsv1.ControllerRevisionHashLabelKey: hash}}, Revision: int64(i + 1)})
				}
			case "jobs":
				objects = append(objects, &batchv1.Job{ObjectMeta: meta, Spec: batchv1.JobSpec{Selector: &metav1.LabelSelector{}}})
				old.OwnerReferences = owner("Job", "web", "previous-root", "batch/v1")
			case "replicasets":
				objects = append(objects, &appsv1.ReplicaSet{ObjectMeta: meta, Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{}}})
				old.OwnerReferences = owner("ReplicaSet", "web", "previous-root", "apps/v1")
			}
			current.OwnerReferences = append([]metav1.OwnerReference(nil), old.OwnerReferences...)
			current.OwnerReferences[0].UID = "root"
			objects = append(objects, old, current)
			client := fake.NewClientset(objects...)
			if err := k8s.InitTestResourceCache(client); err != nil {
				t.Fatal(err)
			}
			pod, _, err := resolveExecPod(t.Context(), client, nil, kind, "shop", "web")
			if err != nil || pod.Name != "current" {
				t.Fatalf("pod=%v err=%v", pod, err)
			}
		})
	}
}

func TestExecPodRefusesOldDeploymentRevision(t *testing.T) {
	defer k8s.ResetTestState()
	controller := true
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "dep", Generation: 2}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{}, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "new"}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 2}}
	var objects []runtime.Object
	objects = append(objects, dep)
	for i, hash := range []string{"old", "new"} {
		rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: hash, Namespace: "shop", UID: types.UID(hash), Annotations: map[string]string{"deployment.kubernetes.io/revision": strconv.Itoa(i + 1)}, Labels: map[string]string{"pod-template-hash": hash}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "dep", Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: *dep.Spec.Template.DeepCopy()}}
		rs.Spec.Template.Spec.Containers[0].Image = hash
		pod := runningPod(hash, hash == "old", "app")
		pod.Labels = map[string]string{"pod-template-hash": hash}
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: hash, UID: types.UID(hash), Controller: &controller}}
		objects = append(objects, rs, pod)
	}
	client := fake.NewClientset(objects...)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	_, _, err := resolveExecPod(t.Context(), client, nil, "deploy", "shop", "web")
	if err == nil || !strings.Contains(err.Error(), "target a specific pod by name to exec into a pod that isn't Ready") {
		t.Fatalf("selected old Ready revision: %v", err)
	}
	// A paused Deployment can observe a new template without creating a ReplicaSet for it.
	dep.Spec.Paused = true
	dep.Spec.Template.Spec.Containers[0].Image = "unreconciled"
	if _, err := client.AppsV1().Deployments("shop").Update(t.Context(), dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, _, err = resolveExecPod(t.Context(), client, nil, "deploy", "shop", "web")
	if err == nil || !strings.Contains(err.Error(), "is paused with no observed desired revision; target a specific pod by name") {
		t.Fatalf("accepted mismatched template: %v", err)
	}
}

// A Deployment template can carry a pod-template-hash label copied from a
// ReplicaSet. The controller ignores that label when it matches ReplicaSets to
// the template, so exec must too, or it can never find the current revision.
func TestExecPodMatchesDeploymentTemplateWithLeakedHash(t *testing.T) {
	defer k8s.ResetTestState()
	controller := true
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web", "pod-template-hash": "stale"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "web:v1"}}}}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "dep", Generation: 3}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{}, Template: template}, Status: appsv1.DeploymentStatus{ObservedGeneration: 3}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-cur", Namespace: "shop", UID: "cur", Annotations: map[string]string{"deployment.kubernetes.io/revision": "3"}, Labels: map[string]string{"pod-template-hash": "cur"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "dep", Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: *template.DeepCopy()}}
	rs.Spec.Template.Labels["pod-template-hash"] = "cur"
	pod := runningPod("web-cur-1", true, "app")
	pod.Labels = map[string]string{"pod-template-hash": "cur"}
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-cur", UID: "cur", Controller: &controller}}
	client := fake.NewClientset(dep, rs, pod)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	got, _, err := resolveExecPod(t.Context(), client, nil, "deploy", "shop", "web")
	if err != nil || got.Name != "web-cur-1" {
		t.Fatalf("pod=%v err=%v", got, err)
	}

	// No ReplicaSet for the template yet: say that, not just "retry".
	dep.Spec.Template.Spec.Containers[0].Image = "web:v2"
	if _, err := client.AppsV1().Deployments("shop").Update(t.Context(), dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, _, err = resolveExecPod(t.Context(), client, nil, "deploy", "shop", "web")
	if err == nil || !strings.Contains(err.Error(), "no ReplicaSet matches its current pod template") {
		t.Fatalf("want a missing-ReplicaSet error, got %v", err)
	}
}

func TestExecPodAuditIncludesCallerCommandAndOutcome(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	ctx := pkgauth.ContextWithUser(t.Context(), &pkgauth.User{Username: "alice", Groups: []string{"operators"}})
	pod := runningPod("web-1", true, "app")
	pod.UID, pod.Spec.NodeName = "selected-uid", "worker-1"
	client := fake.NewClientset(pod)
	var calls []execCall
	_, _, err := execPod(ctx, client, nil, fakeExec(&calls, "", "", k8sexec.CodeExitError{Err: errors.New("exit 2"), Code: 2}), execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"printf", "x\ny"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`[audit] user="alice"`, `ns="shop"`, `name="web-1"`, `container="app"`, `executable="printf"`, `argumentCount=1`, `commandHash=`, `requestedKind="pod"`, `requestedName="web-1"`, `selectedUID="selected-uid"`, `node="worker-1"`, `exitCode=2`, `duration=`, `outcome="exit_2"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %s: %s", want, logs.String())
		}
	}
}

func TestExecPodTimeoutBounds(t *testing.T) {
	for _, seconds := range []int{46, 300, int(^uint(0) >> 1)} {
		_, err := boundedTimeout(seconds, execDefaultTimeout, execMaxTimeout)
		if err == nil {
			t.Fatalf("accepted %d second timeout", seconds)
		}
	}
	for _, seconds := range []int{0, 30, 45} {
		got, err := boundedTimeout(seconds, execDefaultTimeout, execMaxTimeout)
		if err != nil || got > 45*time.Second || got <= 0 {
			t.Fatalf("%d -> %v, %v", seconds, got, err)
		}
	}
}

func TestExecPodDeadlineIncludesResolution(t *testing.T) {
	client := fake.NewClientset()
	release := make(chan struct{})
	defer close(release)
	client.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		<-release
		return true, nil, context.DeadlineExceeded
	})
	start := time.Now()
	var calls []execCall
	_, _, err := execPod(t.Context(), client, nil, fakeExec(&calls, "", "", nil), execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"true"}, TimeoutSeconds: 1})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second || len(calls) != 0 {
		t.Fatalf("elapsed=%s calls=%v error=%v", time.Since(start), calls, err)
	}
}

func TestExecPodTruncatesBothStreamsInBytes(t *testing.T) {
	client := fake.NewClientset(runningPod("web-1", true, "app"))
	var calls []execCall
	output := strings.Repeat("世", execOutputCap)
	result, _, err := execPod(t.Context(), client, nil, fakeExec(&calls, output, output, nil), execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, result)
	for _, stream := range []string{"stdout", "stderr"} {
		if len(got[stream].(string)) > execOutputCap || got[stream+"Truncated"] != true || got[stream+"Bytes"] != float64(len(output)) || !utf8.ValidString(got[stream].(string)) {
			t.Fatalf("%s: %+v", stream, got[stream+"Truncated"])
		}
	}
}

func TestExecPodDeadlineBoundsUnresponsiveExecutor(t *testing.T) {
	client := fake.NewClientset(runningPod("web-1", true, "app"))
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() { close(release); <-finished }()
	run := func(ctx context.Context, namespace, pod, container string, command []string, out, errOut io.Writer) error {
		defer close(finished)
		_, _ = io.WriteString(out, "partial")
		<-release
		_, _ = io.WriteString(out, "late output")
		return nil
	}
	start := time.Now()
	result, _, err := execPod(t.Context(), client, nil, run, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"sleep", "100"}, TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, result)
	if time.Since(start) > 5*time.Second || got["timedOut"] != true || got["exitCode"] != nil || got["stdout"] != "partial" {
		t.Fatalf("elapsed=%s result=%v", time.Since(start), got)
	}
}

func TestExecPodInvalidDefaultAnnotationDoesNotExecute(t *testing.T) {
	pod := runningPod("web-1", true, "app", "proxy")
	pod.Annotations = map[string]string{"kubectl.kubernetes.io/default-container": "missing"}
	var calls []execCall
	_, _, err := execPod(t.Context(), fake.NewClientset(pod), nil, fakeExec(&calls, "", "", nil), execPodInput{Namespace: "shop", Name: pod.Name, Command: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "default-container annotation") || !strings.Contains(err.Error(), "containers: app, proxy") || len(calls) != 0 {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
}

func TestExecOutputRetainsHeadAndTailAcrossWrites(t *testing.T) {
	for _, size := range []int{1, 13, execOutputCap / 2, execOutputCap, execOutputCap * 3} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			input := "initial context\n" + strings.Repeat("x", execOutputCap*3) + "\nfinal diagnostic"
			buffer := &cappedBuffer{limit: execOutputCap}
			for start := 0; start < len(input); start += size {
				chunk := input[start:min(start+size, len(input))]
				if n, err := io.WriteString(buffer, chunk); err != nil || n != len(chunk) {
					t.Fatalf("write: %d %v", n, err)
				}
			}
			result := map[string]any{}
			addTruncation(result, "stdout", buffer)
			output := result["stdout"].(string)
			if len(output) > execOutputCap || !strings.HasPrefix(output, "initial context\n") || !strings.HasSuffix(output, "\nfinal diagnostic") || !strings.Contains(output, "[... output omitted ...]") || result["stdoutBytes"] != len(input) || result["stdoutTruncated"] != true {
				t.Fatalf("bytes=%v len=%d head/tail lost", result["stdoutBytes"], len(output))
			}
		})
	}
}

func TestExecOutputMarksEncodingLossAndBoundsExpansion(t *testing.T) {
	for _, input := range []string{"A\xffB", strings.Repeat("A\xff", execOutputCap/2), strings.Repeat("世", execOutputCap)} {
		buffer := &cappedBuffer{limit: execOutputCap}
		io.WriteString(buffer, input)
		result := map[string]any{}
		addTruncation(result, "stdout", buffer)
		output := result["stdout"].(string)
		if len(output) > execOutputCap || !utf8.ValidString(output) || result["stdoutBytes"] != len(input) {
			t.Fatalf("output bytes=%d raw=%v", len(output), result["stdoutBytes"])
		}
		if utf8.ValidString(input) && result["stdoutEncodingLoss"] != nil {
			t.Fatal("valid UTF-8 marked corrupt at retention boundary")
		}
		if !utf8.ValidString(input) && result["stdoutEncodingLoss"] != true {
			t.Fatal("encoding loss unmarked")
		}
		if input == "A\xffB" && output != "A�B" {
			t.Fatalf("altered evidence: %q", output)
		}
	}
}
