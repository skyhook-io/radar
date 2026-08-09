package reachability

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/probe"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestBuildProbeJob_SecurityInvariants pins the restricted-PSA + self-destruct
// fields and the command. A regression here is a security regression.
func TestBuildProbeJob_SecurityInvariants(t *testing.T) {
	job := buildProbeJob(RunOptions{
		Namespace: "prod", Image: "ghcr.io/skyhook-io/radar:1.2.3",
		Target: "10.96.0.10:8080", Scheme: "https", Host: "api.prod", Layers: "tcp,tls,http",
	})

	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 60 {
		t.Errorf("TTLSecondsAfterFinished = %v, want 60", job.Spec.TTLSecondsAfterFinished)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 25 {
		t.Errorf("ActiveDeadlineSeconds = %v, want 25", job.Spec.ActiveDeadlineSeconds)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Errorf("BackoffLimit = %v, want 0", job.Spec.BackoffLimit)
	}

	ps := job.Spec.Template.Spec
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken != false {
		t.Error("AutomountServiceAccountToken must be explicitly false (no SA token in the probe pod)")
	}
	if ps.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want Never", ps.RestartPolicy)
	}
	if ps.SecurityContext == nil || ps.SecurityContext.RunAsNonRoot == nil || !*ps.SecurityContext.RunAsNonRoot {
		t.Error("pod must RunAsNonRoot")
	}
	if ps.SecurityContext.SeccompProfile == nil || ps.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("pod must set seccomp RuntimeDefault")
	}

	if len(ps.Containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(ps.Containers))
	}
	c := ps.Containers[0]
	sc := c.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("container must set AllowPrivilegeEscalation=false")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("container must set ReadOnlyRootFilesystem=true")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("container must drop ALL capabilities, got %v", sc.Capabilities)
	}
	if c.Image != "ghcr.io/skyhook-io/radar:1.2.3" {
		t.Errorf("image = %q", c.Image)
	}
	cmd := strings.Join(c.Command, " ")
	for _, want := range []string{"/radar", "probe", "--target 10.96.0.10:8080", "--json", "--scheme https", "--host api.prod", "--layers tcp,tls,http"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q; got: %s", want, cmd)
		}
	}
	if c.Resources.Limits.Cpu().String() != "50m" {
		t.Errorf("cpu limit = %s, want 50m", c.Resources.Limits.Cpu())
	}
}

// TestBuildProbeJob_OmitsEmptyFlags: a minimal request only carries --target/--json.
func TestBuildProbeJob_OmitsEmptyFlags(t *testing.T) {
	job := buildProbeJob(RunOptions{Namespace: "ns", Image: "img", Target: "1.2.3.4:80"})
	cmd := strings.Join(job.Spec.Template.Spec.Containers[0].Command, " ")
	for _, no := range []string{"--scheme", "--host", "--path", "--layers"} {
		if strings.Contains(cmd, no) {
			t.Errorf("minimal request should not include %q; got: %s", no, cmd)
		}
	}
}

// The Job command must carry the exact request the user chose - host/scheme/path
// - so the in-cluster probe tests the intended route, not a hardcoded http://svc/.
func TestBuildProbeJob_PassesRouteRequest(t *testing.T) {
	job := buildProbeJob(RunOptions{
		Namespace: "ns", Image: "img",
		Target: "10.0.0.5:8080", Scheme: "https", Host: "shop.example.com", Path: "/api/", Layers: "tcp,http",
	})
	cmd := strings.Join(job.Spec.Template.Spec.Containers[0].Command, " ")
	for _, want := range []string{"--scheme https", "--host shop.example.com", "--path /api/", "--layers tcp,http"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("Job command missing %q; got: %s", want, cmd)
		}
	}
}

// The fallback (copyable) command must mirror the Job's args AND shell-quote
// user-controlled values so a crafted host/path can't break out of the command.
func TestFallbackCommand_ParityAndQuoting(t *testing.T) {
	cmd := FallbackCommand(RunOptions{
		Namespace: "ns", Image: "img",
		Target: "10.0.0.5:8080", Scheme: "https", Host: "shop.example.com", Path: "/api/", Layers: "tcp,http",
	})
	for _, want := range []string{"--target '10.0.0.5:8080'", "--timeout 3s", "--scheme 'https'", "--host 'shop.example.com'", "--path '/api/'", "--layers 'tcp,http'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("fallback missing %q; got: %s", want, cmd)
		}
	}
	evil := FallbackCommand(RunOptions{Namespace: "ns", Image: "img", Target: "x:80", Path: "/a;rm -rf /"})
	if !strings.Contains(evil, "'/a;rm -rf /'") {
		t.Errorf("dangerous path not single-quoted; got: %s", evil)
	}
}

func TestFallbackCommand_TCPHasNoHTTPArgs(t *testing.T) {
	cmd := FallbackCommand(RunOptions{
		Namespace: "ns", Image: "img", Target: "database:6379", Layers: "tcp",
	})
	for _, want := range []string{"--target 'database:6379'", "--timeout 3s", "--layers 'tcp'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("TCP fallback missing %q; got: %s", want, cmd)
		}
	}
	for _, forbidden := range []string{"--scheme", "--host", "--path"} {
		if strings.Contains(cmd, forbidden) {
			t.Errorf("TCP fallback contains HTTP-only %q; got: %s", forbidden, cmd)
		}
	}
}

// The timeout must tell the user WHY, not just "didn't finish": injected init
// containers, unschedulable, or image-pull are the generic startup blocks.
func TestProbeTimeoutError_NamesTheCause(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{"nil pod (never observed)", nil, "never saw the probe pod run"},
		{"injected init containers", &corev1.Pod{Status: corev1.PodStatus{
			Phase:                 corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "datadog-init", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		}}, "admission webhook injected"},
		{"unschedulable", &corev1.Pod{Status: corev1.PodStatus{
			Phase:      corev1.PodPending,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Message: "0/1 nodes are available"}},
		}}, "couldn't be scheduled"},
		{"image pull", &corev1.Pod{Status: corev1.PodStatus{
			Phase:             corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "probe", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}},
		}}, "ImagePullBackOff"},
	}
	for _, c := range cases {
		got := probeTimeoutError(c.pod, nil, 0).Error()
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: error = %q, want it to mention %q", c.name, got, c.want)
		}
	}
	healthy := &corev1.Pod{Status: corev1.PodStatus{
		Phase:                 corev1.PodRunning,
		InitContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}},
	}}
	if r := podStartupBlock(healthy); r != "" {
		t.Errorf("a finished init container should not block; got %q", r)
	}
}

// TestProbeTimeoutError_ImagePullIsInfraFailure pins the image-pull
// classification: the probe container failing to pull is probe INFRASTRUCTURE
// failing to run - the message must read as couldn't-run/not-tested (never
// "probe failed" or a bare timeout) and must name the --reachability-image
// remedy. Scoped to the probe container: an injected sidecar's pull failure
// stays on the generic sidecar-attribution path.
func TestProbeTimeoutError_ImagePullIsInfraFailure(t *testing.T) {
	for _, reason := range []string{"ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ImageInspectError"} {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			Phase:             corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "probe", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}},
		}}
		got := probeTimeoutError(pod, nil, 0).Error()
		if !strings.Contains(got, "couldn't run") || !strings.Contains(got, "NOT tested") {
			t.Errorf("%s: must read as couldn't-run/not-tested, got %q", reason, got)
		}
		if !strings.Contains(got, reason) {
			t.Errorf("%s: must name the kubelet reason, got %q", reason, got)
		}
		if !strings.Contains(got, "--reachability-image") {
			t.Errorf("%s: must name the remedy flag, got %q", reason, got)
		}
		if strings.Contains(got, "probe pod failed") {
			t.Errorf("%s: infra failure must not read as a probe FAILURE, got %q", reason, got)
		}
	}
	// A sidecar's pull failure is NOT the probe infrastructure's fault - it keeps
	// the generic sidecar attribution, without the private-registry remedy.
	sidecar := &corev1.Pod{Status: corev1.PodStatus{
		Phase:             corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{Name: "vault-agent", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}},
	}}
	got := probeTimeoutError(sidecar, nil, 0).Error()
	if strings.Contains(got, "--reachability-image") || !strings.Contains(got, "sidecar") {
		t.Errorf("sidecar pull failure must keep the sidecar attribution, got %q", got)
	}
	// A non-image-pull waiting reason on the probe container keeps the generic
	// startup-block wording.
	creating := &corev1.Pod{Status: corev1.PodStatus{
		Phase:             corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{Name: "probe", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}},
	}}
	if got := probeTimeoutError(creating, nil, 0).Error(); strings.Contains(got, "--reachability-image") {
		t.Errorf("non-pull waiting reason must not claim an image-pull cause, got %q", got)
	}
}

// TestProbeTimeoutError_BudgetTightenedNamesTheBudget: a run that started with
// less request budget than jobTimeout must attribute its expiry to the REQUEST
// BUDGET, not to cluster slowness - "the pod was pending, try again" sends the
// operator chasing a scheduling problem that doesn't exist. Pod-state detail is
// kept when available; probe-infra image-pull failure still outranks the budget
// framing (a full-length run would have failed the same way).
func TestProbeTimeoutError_BudgetTightenedNamesTheBudget(t *testing.T) {
	pending := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}
	got := probeTimeoutError(pending, nil, 9*time.Second).Error()
	for _, want := range []string{"request time budget", "9s granted", "re-run"} {
		if !strings.Contains(got, want) {
			t.Errorf("budget-tightened timeout must mention %q, got %q", want, got)
		}
	}
	if !strings.Contains(got, "pending") {
		t.Errorf("pod-state detail must be kept when available, got %q", got)
	}
	if strings.Contains(got, "try again, or run the command below") {
		t.Errorf("budget expiry must not read as a generic cluster timeout, got %q", got)
	}

	// Startup-block detail (e.g. unschedulable) is kept alongside the budget framing.
	unschedulable := &corev1.Pod{Status: corev1.PodStatus{
		Phase:      corev1.PodPending,
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Message: "0/1 nodes are available"}},
	}}
	got = probeTimeoutError(unschedulable, nil, 12*time.Second).Error()
	if !strings.Contains(got, "request time budget") || !strings.Contains(got, "couldn't be scheduled") {
		t.Errorf("budget framing must keep the startup-block detail, got %q", got)
	}

	// Never-observed pod: still a budget story, no pod detail to add.
	got = probeTimeoutError(nil, nil, 7*time.Second).Error()
	if !strings.Contains(got, "request time budget") || strings.Contains(got, "never saw the probe pod run") {
		t.Errorf("nil pod under a tight budget must blame the budget, got %q", got)
	}

	// Image-pull infra failure outranks the budget attribution.
	pullBlocked := &corev1.Pod{Status: corev1.PodStatus{
		Phase:             corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{Name: "probe", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}},
	}}
	got = probeTimeoutError(pullBlocked, nil, 10*time.Second).Error()
	if !strings.Contains(got, "ImagePullBackOff") || strings.Contains(got, "request time budget") {
		t.Errorf("image-pull failure must outrank the budget framing, got %q", got)
	}

	// A zero budget (run started with >= jobTimeout available) keeps the existing
	// generic attribution.
	if got := probeTimeoutError(pending, nil, 0).Error(); strings.Contains(got, "request time budget") {
		t.Errorf("full-budget timeout must not claim a budget cause, got %q", got)
	}
}

// TestProbeContainerTerminated_NameScoped pins that completion gates on the
// "probe" container only - a sidecar that runs forever (mesh) or terminates must
// never be read as the probe's own completion signal.
func TestProbeContainerTerminated_NameScoped(t *testing.T) {
	probeDone := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "istio-proxy", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: "probe", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
	}}}
	if term := probeContainerTerminated(probeDone); term == nil || term.ExitCode != 0 {
		t.Errorf("probe terminated (exit 0) must be detected; got %+v", term)
	}
	// Probe still Running while a sidecar already terminated → NOT complete.
	probeRunning := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "istio-proxy", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
		{Name: "probe", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
	}}}
	if term := probeContainerTerminated(probeRunning); term != nil {
		t.Errorf("probe still running must read as not-terminated; got %+v", term)
	}
	// No probe container status at all → nil.
	if term := probeContainerTerminated(&corev1.Pod{}); term != nil {
		t.Errorf("no probe status must be nil; got %+v", term)
	}
}

// TestProbeFailedError_AlwaysHasCause pins that a probe failure never produces an
// empty message: logs win, else the terminated reason/exit code, else a fallback.
func TestProbeFailedError_AlwaysHasCause(t *testing.T) {
	if got := probeFailedError("connection refused", &corev1.ContainerStateTerminated{ExitCode: 1}).Error(); !strings.Contains(got, "connection refused") {
		t.Errorf("logs present: want them surfaced; got %q", got)
	}
	if got := probeFailedError("", &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}).Error(); !strings.Contains(got, "OOMKilled") || !strings.Contains(got, "137") {
		t.Errorf("empty logs + reason: want reason+code; got %q", got)
	}
	if got := probeFailedError("", &corev1.ContainerStateTerminated{ExitCode: 2}).Error(); !strings.Contains(got, "exit code 2") {
		t.Errorf("empty logs, no reason: want exit code; got %q", got)
	}
	if got := probeFailedError("", nil).Error(); got == "" {
		t.Error("empty logs + nil term must still carry a message, never empty")
	}
}

// TestPodStartupBlock_SidecarNotBlamed pins that a waiting INJECTED sidecar is
// attributed to the sidecar, never misreported as the probe container failing.
func TestPodStartupBlock_SidecarNotBlamed(t *testing.T) {
	sidecarStuck := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "vault-agent", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
	}}}
	got := podStartupBlock(sidecarStuck)
	if !strings.Contains(got, "sidecar") || !strings.Contains(got, "vault-agent") {
		t.Errorf("waiting sidecar must be named as a sidecar; got %q", got)
	}
	if strings.Contains(got, "the probe container couldn't start") {
		t.Errorf("must not blame the probe container for a sidecar; got %q", got)
	}
	probeStuck := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "probe", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
	}}}
	if got := podStartupBlock(probeStuck); !strings.Contains(got, "probe container") {
		t.Errorf("waiting probe container must be named; got %q", got)
	}
}

func TestParseProbeOutput_StampsInClusterVantage(t *testing.T) {
	raw := `[{"layer":"tcp","target":"10.96.0.10:80","vantage":"local","ok":true},
	         {"layer":"http","target":"http://10.96.0.10:80/","ok":true,"tone":"healthy","detail":"HTTP 200"}]`
	res, err := parseProbeOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %d", len(res))
	}
	for _, r := range res {
		if r.Vantage != probe.VantageInCluster {
			t.Errorf("vantage = %q, want in-cluster (results came from inside the dataplane)", r.Vantage)
		}
	}
	if res[1].Tone != probe.ToneHealthy {
		t.Errorf("tone preserved? got %q", res[1].Tone)
	}
}

func TestParseProbeOutput_RejectsGarbage(t *testing.T) {
	if _, err := parseProbeOutput("panic: something went wrong\n"); err == nil {
		t.Error("non-JSON probe output must be an error, not silently empty")
	}
}

// TestCapability_GatesEveryVerb pins the widened capability gate: the runner needs
// create-jobs AND list-pods AND get-pods/log, and a denial of ANY one blocks
// (naming the missing verb) rather than letting the probe spin to a silent timeout.
func TestCapability_GatesEveryVerb(t *testing.T) {
	clientThatAllows := func(allow func(a *authzv1.ResourceAttributes) bool) *fake.Clientset {
		c := fake.NewSimpleClientset()
		c.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview)
			ssar.Status.Allowed = allow(ssar.Spec.ResourceAttributes)
			return true, ssar, nil
		})
		return c
	}
	if ok, reason, err := Capability(context.Background(), clientThatAllows(func(*authzv1.ResourceAttributes) bool { return true }), "ns"); !ok || reason != "" || err != nil {
		t.Errorf("all-allowed: ok=%v reason=%q err=%v, want allowed", ok, reason, err)
	}
	cases := []struct {
		name   string
		deny   func(a *authzv1.ResourceAttributes) bool
		expect string
	}{
		{"no create jobs", func(a *authzv1.ResourceAttributes) bool { return a.Resource == "jobs" }, "create Jobs"},
		{"no list pods", func(a *authzv1.ResourceAttributes) bool { return a.Resource == "pods" && a.Subresource == "" }, "list Pods"},
		{"no read logs", func(a *authzv1.ResourceAttributes) bool { return a.Resource == "pods" && a.Subresource == "log" }, "read Pod logs"},
	}
	for _, tc := range cases {
		client := clientThatAllows(func(a *authzv1.ResourceAttributes) bool { return !tc.deny(a) })
		ok, reason, err := Capability(context.Background(), client, "ns")
		if err != nil {
			t.Fatalf("%s: unexpected err %v", tc.name, err)
		}
		if ok || !strings.Contains(reason, tc.expect) {
			t.Errorf("%s: ok=%v reason=%q, want denied naming %q", tc.name, ok, reason, tc.expect)
		}
	}
}

func radarPod(ns string, containers ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "radar-abc", Namespace: ns},
		Spec:       corev1.PodSpec{Containers: containers},
	}
}

// TestResolveImage_Precedence pins the four-source order: explicit override >
// self-read (radar's live pod image) > RADAR_IMAGE env > version default.
func TestResolveImage_Precedence(t *testing.T) {
	old := DefaultImageRef
	DefaultImageRef = "ghcr.io/skyhook-io/radar:vdefault"
	defer func() { DefaultImageRef = old }()

	client := fake.NewSimpleClientset(radarPod("radar-ns",
		corev1.Container{Name: "istio-proxy", Image: "proxy:1"},
		corev1.Container{Name: "radar", Image: "myregistry.internal/radar@sha256:dead"},
	))
	ctx := context.Background()
	t.Setenv("MY_POD_NAME", "radar-abc")
	t.Setenv("MY_POD_NAMESPACE", "radar-ns")
	t.Setenv("RADAR_IMAGE", "env/radar:x")

	if got := ResolveImage(ctx, client, "override/radar:pin"); got != "override/radar:pin" {
		t.Errorf("override should win, got %q", got)
	}
	if got := ResolveImage(ctx, client, ""); got != "myregistry.internal/radar@sha256:dead" {
		t.Errorf("self-read (live pod image) should win over env/default, got %q", got)
	}
	t.Setenv("MY_POD_NAME", "") // self-read can't run
	if got := ResolveImage(ctx, client, ""); got != "env/radar:x" {
		t.Errorf("RADAR_IMAGE should apply when self-read unavailable, got %q", got)
	}
	t.Setenv("RADAR_IMAGE", "")
	if got := ResolveImage(ctx, client, ""); got != "ghcr.io/skyhook-io/radar:vdefault" {
		t.Errorf("version default should apply last, got %q", got)
	}

	// The configured --reachability-image override (recorded at startup) wins over
	// self-read / RADAR_IMAGE / default even with an EMPTY per-call override arg -
	// this is the MCP path, which must honor the operator's override too.
	SetConfiguredImage("configured/radar:flag")
	defer SetConfiguredImage("")
	t.Setenv("MY_POD_NAME", "radar-abc") // self-read would otherwise win
	t.Setenv("RADAR_IMAGE", "env/radar:x")
	if got := ResolveImage(ctx, client, ""); got != "configured/radar:flag" {
		t.Errorf("configured override should beat self-read/env/default, got %q", got)
	}
	if got := ResolveImage(ctx, client, "explicit/radar:arg"); got != "explicit/radar:arg" {
		t.Errorf("an explicit per-call override arg should still beat the configured one, got %q", got)
	}
}

// TestSelfPodImage covers the self-read helper's container pick + every
// fall-through-to-empty case (so resolution drops cleanly to the next source).
func TestSelfPodImage(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MY_POD_NAME", "radar-abc")
	t.Setenv("MY_POD_NAMESPACE", "radar-ns")

	// the "radar" container is picked even when not first
	c := fake.NewSimpleClientset(radarPod("radar-ns",
		corev1.Container{Name: "linkerd-proxy", Image: "proxy:1"},
		corev1.Container{Name: "radar", Image: "radar:real"},
	))
	if got := selfPodImage(ctx, c); got != "radar:real" {
		t.Errorf("want the radar container image, got %q", got)
	}
	// first-container fallback when there's no "radar" container
	c2 := fake.NewSimpleClientset(radarPod("radar-ns", corev1.Container{Name: "app", Image: "app:1"}))
	if got := selfPodImage(ctx, c2); got != "app:1" {
		t.Errorf("want first-container fallback, got %q", got)
	}
	if got := selfPodImage(ctx, nil); got != "" {
		t.Errorf("nil client should yield empty, got %q", got)
	}
	if got := selfPodImage(ctx, fake.NewSimpleClientset()); got != "" {
		t.Errorf("pod-not-found (Get error) should yield empty, got %q", got)
	}
	// multi-container pod with NO "radar" container → empty (don't guess a sidecar
	// image, which lacks /radar); only a SOLE container is trusted as the fallback.
	multi := fake.NewSimpleClientset(radarPod("radar-ns",
		corev1.Container{Name: "istio-proxy", Image: "proxy:1"},
		corev1.Container{Name: "app", Image: "app:2"},
	))
	if got := selfPodImage(ctx, multi); got != "" {
		t.Errorf("multi-container no-radar pod should yield empty (not a sidecar guess), got %q", got)
	}
	t.Setenv("MY_POD_NAME", "")
	if got := selfPodImage(ctx, c); got != "" {
		t.Errorf("missing downward-API env should yield empty, got %q", got)
	}
}

// A dev build's git-describe tag never exists on the registry, so main()
// installs LatestReleaseImage to fall back to the newest published release.
// It must rank BELOW every explicit source (override, self-read, RADAR_IMAGE)
// and above only the version default; a failed lookup ("") keeps the default.
func TestResolveImage_DevFallsBackToLatestRelease(t *testing.T) {
	old := DefaultImageRef
	DefaultImageRef = "ghcr.io/skyhook-io/radar:1.10.3-78-gabc123"
	defer func() { DefaultImageRef = old }()
	t.Setenv("MY_POD_NAME", "")
	t.Setenv("RADAR_IMAGE", "")

	LatestReleaseImage = func() string { return "ghcr.io/skyhook-io/radar:1.10.3" }
	defer func() { LatestReleaseImage = nil }()
	if got := ResolveImage(context.Background(), nil, ""); got != "ghcr.io/skyhook-io/radar:1.10.3" {
		t.Errorf("dev build should fall back to the latest published release, got %q", got)
	}

	t.Setenv("RADAR_IMAGE", "env/radar:x")
	if got := ResolveImage(context.Background(), nil, ""); got != "env/radar:x" {
		t.Errorf("RADAR_IMAGE must still outrank the latest-release fallback, got %q", got)
	}
	t.Setenv("RADAR_IMAGE", "")

	LatestReleaseImage = func() string { return "" }
	if got := ResolveImage(context.Background(), nil, ""); got != DefaultImageRef {
		t.Errorf("a failed release lookup keeps the version default, got %q", got)
	}
}
