package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// execToolsEnabled registers the tools that run commands in the cluster
// (exec_pod, run_pod) on the full server. They reach whatever a container can
// reach, so they stay off unless the operator passes --mcp-exec-tools.
var execToolsEnabled atomic.Bool

// SetExecToolsEnabled turns the exec tools on or off for servers built
// afterwards. Call it before NewHandler.
func SetExecToolsEnabled(enabled bool) { execToolsEnabled.Store(enabled) }

const (
	execDefaultTimeout = 30 * time.Second
	execMaxTimeout     = 300 * time.Second
	// execOutputCap bounds each of stdout and stderr. The head is kept: probe
	// output, status lines and first errors come first.
	execOutputCap = 64 << 10
)

type execPodInput struct {
	Namespace      string   `json:"namespace" jsonschema:"namespace of the pod or workload"`
	Name           string   `json:"name" jsonschema:"pod name, or the workload name when kind is set"`
	Kind           string   `json:"kind,omitempty" jsonschema:"pod (default), or deployment, statefulset, daemonset, job, replicaset, rollout: run in one Running pod of that workload, preferring a Ready one, as kubectl exec deploy/<name> does"`
	Container      string   `json:"container,omitempty" jsonschema:"container name; default is the pod's kubectl.kubernetes.io/default-container annotation, else its first container"`
	Command        []string `json:"command" jsonschema:"argv to run, not a shell line. Use [\"sh\",\"-c\",\"<script>\"] for pipes, redirects or variables. The binary must exist in the container image"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty" jsonschema:"default 30, max 300. At the limit Radar stops waiting and returns the output so far; the process may keep running in the container"`
}

// execStreams runs one command in a container, writing its output to stdout
// and stderr. Production uses the caller's impersonated client; tests stand in
// for the cluster.
type execStreams func(ctx context.Context, namespace, pod, container string, command []string, stdout, stderr io.Writer) error

// registerExecTools adds the tools that run commands in the cluster. Each
// call runs as the caller: the API server checks pods/exec, or pod create,
// against the caller's RBAC.
func registerExecTools(server *mcp.Server, paramRegistry *toolParamRegistry, annotations *mcp.ToolAnnotations) {
	addToolWithRegistry(paramRegistry, server, &mcp.Tool{
		Name: "exec_pod",
		Description: "Run one non-interactive command in a running container and return its stdout, stderr and exit code, like kubectl exec <pod> -- <cmd>. " +
			"Use it for checks only the workload's own network or files can answer: an HTTP call from the client pod to its dependency, " +
			"a database or queue CLI (psql, redis-cli, kafka-consumer-groups.sh), a /metrics endpoint, the env or config a process actually sees. " +
			"Target a pod by name, or a workload with kind (deployment, statefulset, ...) to use one of its Running pods, Ready first. " +
			"command is an argv array; wrap pipes or variables in [\"sh\",\"-c\",\"...\"]. No stdin, no TTY. " +
			"Output is capped at 64KB per stream and the call at timeout_seconds (default 30, max 300). " +
			"A non-zero exit code is a result, not a tool error. Commands can change state inside the container; prefer read-only commands.",
		Annotations: annotations,
	}, logToolCall("exec_pod", handleExecPod))
}

func handleExecPod(ctx context.Context, req *mcp.CallToolRequest, input execPodInput) (*mcp.CallToolResult, any, error) {
	if err := requireConnected(); err != nil {
		return nil, nil, err
	}
	client := k8s.ClientFromContext(ctx)
	config := k8s.ConfigFromContext(ctx)
	if client == nil || config == nil {
		return nil, nil, errNotConnected()
	}
	run := func(ctx context.Context, namespace, pod, container string, command []string, stdout, stderr io.Writer) error {
		executor, err := k8score.NewPodCommandExecutor(client, config, namespace, pod, container, command)
		if err != nil {
			return err
		}
		return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr})
	}
	return execPod(ctx, client, run, input)
}

func execPod(ctx context.Context, client kubernetes.Interface, run execStreams, input execPodInput) (*mcp.CallToolResult, any, error) {
	namespace, name := strings.TrimSpace(input.Namespace), strings.TrimSpace(input.Name)
	if namespace == "" || name == "" {
		return nil, nil, fmt.Errorf("namespace and name are required")
	}
	if len(input.Command) == 0 || strings.TrimSpace(input.Command[0]) == "" {
		return nil, nil, fmt.Errorf("command is required, as an argv array such as [\"sh\",\"-c\",\"<script>\"]")
	}
	timeout, err := boundedTimeout(input.TimeoutSeconds, execDefaultTimeout, execMaxTimeout)
	if err != nil {
		return nil, nil, err
	}

	// One deadline covers finding the pod and running the command.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pod, picked, err := resolveExecPod(runCtx, client, input.Kind, namespace, name)
	if err != nil {
		return nil, nil, err
	}
	container := strings.TrimSpace(input.Container)
	if container == "" {
		container = k8score.DefaultExecContainer(pod)
	} else if !podHasContainer(pod, container) {
		return nil, nil, fmt.Errorf("container %q not found in pod %s/%s; containers: %s", container, namespace, pod.Name, strings.Join(podContainerNames(pod), ", "))
	}

	auditRequest := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
	audit := auth.AuditActionDetails{Action: "exec_pod", Namespace: namespace, Name: pod.Name, Container: container, Source: "mcp", Outcome: "failed"}
	defer func() { auth.AuditLogAction(auditRequest, audit) }()

	stdout, stderr := &cappedBuffer{limit: execOutputCap}, &cappedBuffer{limit: execOutputCap}
	start := time.Now()
	streamErr := run(runCtx, namespace, pod.Name, container, input.Command, stdout, stderr)
	elapsed := time.Since(start)

	result := map[string]any{
		"namespace":  namespace,
		"pod":        pod.Name,
		"container":  container,
		"stdout":     stdout.String(),
		"stderr":     stderr.String(),
		"durationMs": elapsed.Milliseconds(),
	}
	if picked != "" {
		result["podSelection"] = picked
	}
	addTruncation(result, "stdout", stdout)
	addTruncation(result, "stderr", stderr)

	var exitErr k8sexec.ExitError
	switch {
	case streamErr == nil:
		result["exitCode"] = 0
		audit.Outcome = "exit_0"
	case errors.As(streamErr, &exitErr):
		result["exitCode"] = exitErr.ExitStatus()
		audit.Outcome = fmt.Sprintf("exit_%d", exitErr.ExitStatus())
	case runCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil:
		result["timedOut"] = true
		result["exitCode"] = nil
		result["message"] = fmt.Sprintf("no exit after %ds; output so far is above. The process may still be running in the container", int(timeout.Seconds()))
		audit.Outcome = "timeout"
	default:
		// The command never ran or the stream broke: RBAC denial, a missing
		// binary, a container that isn't running. Partial output, if any, is
		// in the error so a broken stream isn't read as a clean empty run.
		msg := fmt.Sprintf("exec in %s/%s container %s failed: %v", namespace, pod.Name, container, streamErr)
		if out := strings.TrimSpace(stderr.String() + stdout.String()); out != "" {
			msg += "; output: " + truncateRunes(out, 500)
		}
		return nil, nil, errors.New(msg)
	}
	return toJSONResult(result)
}

// resolveExecPod returns the pod to exec into. For kind=pod it is the named
// pod. For a workload it is one of its pods, Running and Ready first, the way
// kubectl exec deploy/<name> picks; picked explains the choice.
func resolveExecPod(ctx context.Context, client kubernetes.Interface, kind, namespace, name string) (*corev1.Pod, string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "pod", "pods", "po":
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", fmt.Errorf("failed to read pod %s/%s: %w", namespace, name, err)
		}
		if pod.Status.Phase != corev1.PodRunning {
			return nil, "", fmt.Errorf("pod %s/%s is %s, not Running; exec needs a running container", namespace, name, pod.Status.Phase)
		}
		return pod, "", nil
	}
	workloadKind := execWorkloadKind(kind)
	if workloadKind == "" {
		return nil, "", fmt.Errorf("invalid kind %q: must be pod, deployment, statefulset, daemonset, job, replicaset, or rollout", kind)
	}
	if !checkNamespaceAccess(ctx, namespace) {
		return nil, "", fmt.Errorf("forbidden: no access to namespace %q", namespace)
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, "", errNotConnected()
	}
	pods, err := k8s.WorkloadPods(ctx, cache, workloadKind, namespace, name)
	if err != nil {
		return nil, "", workloadSelectorMCPError(ctx, err, workloadKind, namespace, name)
	}
	pod := pickExecPod(pods)
	if pod == nil {
		phases := make([]string, 0, len(pods))
		for _, p := range pods {
			phases = append(phases, fmt.Sprintf("%s=%s", p.Name, p.Status.Phase))
		}
		if len(phases) == 0 {
			return nil, "", fmt.Errorf("%s %s/%s has no pods to exec into", workloadKind, namespace, name)
		}
		return nil, "", fmt.Errorf("%s %s/%s has no Running pod to exec into: %s", workloadKind, namespace, name, strings.Join(phases, ", "))
	}
	picked := fmt.Sprintf("%s of %d pods of %s %s", pod.Name, len(pods), workloadKind, name)
	if !podReady(pod) {
		picked += "; no pod is Ready, so this one is Running but not Ready"
	}
	return pod, picked, nil
}

// execWorkloadKind maps the kinds kubectl exec accepts, short names included,
// to the plural WorkloadPods takes; "" for anything else.
func execWorkloadKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deploy", "deployment", "deployments":
		return "deployments"
	case "sts", "statefulset", "statefulsets":
		return "statefulsets"
	case "ds", "daemonset", "daemonsets":
		return "daemonsets"
	case "job", "jobs":
		return "jobs"
	case "rs", "replicaset", "replicasets":
		return "replicasets"
	case "rollout", "rollouts":
		return "rollouts"
	}
	return ""
}

// pickExecPod chooses a Running, not-terminating pod, Ready ones first, then
// the newest. Returns nil when none is Running.
func pickExecPod(pods []*corev1.Pod) *corev1.Pod {
	candidates := make([]*corev1.Pod, 0, len(pods))
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ri, rj := podReady(candidates[i]), podReady(candidates[j])
		if ri != rj {
			return ri
		}
		return candidates[i].CreationTimestamp.After(candidates[j].CreationTimestamp.Time)
	})
	return candidates[0]
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podHasContainer(pod *corev1.Pod, name string) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == name {
			return true
		}
	}
	return false
}

func podContainerNames(pod *corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	return names
}

// cappedBuffer keeps the first limit bytes written and counts the rest. Write
// never fails, so a chatty command isn't cut off mid-stream by the cap.
type cappedBuffer struct {
	limit int
	buf   []byte
	total int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if room := b.limit - len(b.buf); room > 0 {
		if len(p) > room {
			b.buf = append(b.buf, p[:room]...)
		} else {
			b.buf = append(b.buf, p...)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return strings.ToValidUTF8(string(b.buf), "") }

func (b *cappedBuffer) truncated() bool { return b.total > len(b.buf) }

func addTruncation(result map[string]any, stream string, b *cappedBuffer) {
	if b.truncated() {
		result[stream+"Truncated"] = fmt.Sprintf("kept the first %d of %d bytes", len(b.buf), b.total)
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
