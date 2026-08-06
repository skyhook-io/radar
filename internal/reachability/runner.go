// Package reachability runs a network reachability probe from INSIDE the cluster
// by creating a short-lived, restricted, self-destructing Job that runs
// `radar probe`. It is the only mutating action in the diagnostics surface and is
// hemmed in: it runs as the CALLER's RBAC (the passed impersonated client), gates
// on a real capability check BEFORE creating anything, and degrades to a copyable
// kubectl command where the caller can't create Jobs. The REST handler and the MCP
// diagnose tool both call into here so the security envelope has one definition.
package reachability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/probe"
	authzv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DefaultImageRef is the container image for the in-cluster probe Job when no
// override is set. main() overrides the tag with the build version so a released
// binary defaults to its matching image.
var DefaultImageRef = "ghcr.io/skyhook-io/radar:dev"

// configuredImage holds the operator's explicit --reachability-image / config
// value, recorded once at startup via SetConfiguredImage. It lets BOTH the REST
// and MCP paths honor the override even though MCP has no access to the server
// config (the MCP path passes an empty override arg).
var configuredImage string

// LatestReleaseImage, when set, names the most recent PUBLISHED radar image.
// main() installs it only for dev-shaped versions (git-describe tags), whose
// version-matched image never exists on the registry - without this, every
// in-cluster test from a locally-built binary ImagePullBackOffs. Released
// binaries keep their exact version-matched default and never consult it.
// Returns "" when the release lookup fails, which falls through to
// DefaultImageRef so the (informative) pull error still names the situation.
var LatestReleaseImage func() string

// SetConfiguredImage records the operator's --reachability-image override so
// ResolveImage honors it regardless of which path (REST/MCP) resolves the image.
func SetConfiguredImage(img string) { configuredImage = img }

// ResolveImage picks the image for the probe Job, most-trusted source first:
//
//  1. override - an explicit --reachability-image / config value (the per-call
//     arg, or the startup-recorded configuredImage). The operator said exactly
//     this, so it always wins, on every path.
//  2. self-read - radar's OWN running pod image, read via selfClient (radar's
//     service-account client, NOT the caller's impersonated one). This is the
//     honest in-cluster default: the probe runs the SAME image as radar itself,
//     correct automatically for private registries, mirrors, and digest-pinned
//     deploys because it reads the live container image.
//  3. RADAR_IMAGE env - a no-RBAC fallback (the Helm chart sets it to radar's
//     deployed image) for when self-read can't run (no get-pods, not a pod).
//  4. DefaultImageRef - the version-matched published image.
//
// selfClient may be nil (callers without a base client) - self-read is skipped.
func ResolveImage(ctx context.Context, selfClient kubernetes.Interface, override string) string {
	if override != "" {
		return override
	}
	if configuredImage != "" {
		return configuredImage
	}
	if img := selfPodImage(ctx, selfClient); img != "" {
		return img
	}
	if env := os.Getenv("RADAR_IMAGE"); env != "" {
		return env
	}
	if LatestReleaseImage != nil {
		if img := LatestReleaseImage(); img != "" {
			return img
		}
	}
	return DefaultImageRef
}

// DefaultImage is the client-less resolution (RADAR_IMAGE env → version default).
// Prefer ResolveImage when a base client is available so the in-cluster self-read
// can run.
func DefaultImage() string {
	return ResolveImage(context.Background(), nil, "")
}

// selfPodImage reads radar's own pod (named by the MY_POD_NAME / MY_POD_NAMESPACE
// downward-API env the chart injects) and returns the image of the "radar"
// container, or the first container. Returns "" when the env is unset, the client
// is nil, the Get fails (e.g. no get-pods RBAC), or the pod has no containers -
// every such case falls through to the next image source.
func selfPodImage(ctx context.Context, selfClient kubernetes.Interface) string {
	name, namespace := os.Getenv("MY_POD_NAME"), os.Getenv("MY_POD_NAMESPACE")
	if name == "" || namespace == "" || selfClient == nil {
		return ""
	}
	pod, err := selfClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || pod == nil || len(pod.Spec.Containers) == 0 {
		return ""
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == "radar" {
			return c.Image
		}
	}
	// No container named "radar": trust the first image ONLY when it is the SOLE
	// container. A multi-container pod (injected mesh sidecar, or radar wrapped in
	// another app) could put a non-radar image first - that image lacks /radar, so
	// fall through to RADAR_IMAGE / the version default instead of guessing wrong.
	if len(pod.Spec.Containers) == 1 {
		return pod.Spec.Containers[0].Image
	}
	return ""
}

// jobTimeout bounds the whole create→run→read cycle; ActiveDeadlineSeconds on the
// Job is the in-cluster backstop and TTLSecondsAfterFinished cleans up even if the
// caller dies mid-wait.
const jobTimeout = 25 * time.Second

// CapabilityError is returned by Run when the caller lacks one of the verbs the
// runner needs (create jobs / list pods / read logs). Callers map it to a 403 /
// "not allowed" path; the Reason names the first missing verb.
type CapabilityError struct{ Reason string }

func (e *CapabilityError) Error() string { return e.Reason }

// RunOptions is one in-cluster probe request: the dial target plus the concrete
// HTTP request to make (scheme/host/path) and which layers to test.
type RunOptions struct {
	Image     string
	Namespace string
	Target    string // clusterIP:port (or host:port)
	Host      string
	Scheme    string
	Path      string
	Layers    string
}

// capabilityCheck is one (group, resource, subresource, verb) the runner needs,
// with a human label for the denial message.
type capabilityCheck struct {
	group, resource, subresource, verb, label string
}

// Capability verifies the caller can do EVERY operation the runner performs:
// create the Job, list the pods it spawns, AND read their logs. The gate is
// all-or-nothing on purpose - a caller who can create a Job but not list pods or
// read logs would otherwise watch the probe spin silently to a timeout with no
// cause shown. When denied, the reason names the first missing verb. Runs as the
// already-impersonated caller's client - the authoritative check.
func Capability(ctx context.Context, client kubernetes.Interface, namespace string) (allowed bool, reason string, err error) {
	for _, c := range []capabilityCheck{
		{group: "batch", resource: "jobs", verb: "create", label: "create Jobs"},
		{resource: "pods", verb: "list", label: "list Pods"},
		{resource: "pods", subresource: "log", verb: "get", label: "read Pod logs"},
	} {
		ok, err := canI(ctx, client, namespace, c)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, "you don't have permission to " + c.label + " in namespace " + namespace, nil
		}
	}
	return true, "", nil
}

// Run executes one in-cluster probe end-to-end: gate → create the restricted Job →
// wait for its pod → read the probe results from its logs → best-effort delete.
// Returns the parsed results, OR a plain-English fallbackCommand the caller can run
// by hand plus an error explaining why the probe didn't complete. The caller must
// pass an impersonated client; all RBAC is enforced by the apiserver against it.
func Run(ctx context.Context, client kubernetes.Interface, opts RunOptions) (results []probe.Result, fallbackCommand string, err error) {
	fallbackCommand = FallbackCommand(opts)
	if client == nil {
		return nil, fallbackCommand, fmt.Errorf("cluster client not available")
	}
	allowed, reason, err := Capability(ctx, client, opts.Namespace)
	if err != nil {
		return nil, fallbackCommand, fmt.Errorf("couldn't verify permission to run an in-cluster test: %w", err)
	}
	if !allowed {
		return nil, fallbackCommand, &CapabilityError{Reason: reason}
	}

	// When the caller's remaining request budget is tighter than jobTimeout
	// (a run late in a multi-route request), record the granted budget so an
	// expiry is attributed to the request budget - not misread as the cluster
	// being slow to start the pod.
	grantedBudget := time.Duration(0)
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining < jobTimeout {
			grantedBudget = remaining
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	job := buildProbeJob(opts)
	created, err := client.BatchV1().Jobs(opts.Namespace).Create(runCtx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fallbackCommand, fmt.Errorf("couldn't create the probe Job: %w", err)
	}
	defer func() {
		// Background() so cleanup survives request cancellation / the runCtx
		// deadline, but BOUNDED so a hung apiserver Delete can't block the handler
		// goroutine indefinitely. TTLSecondsAfterFinished is the backstop, so a
		// timeout here is harmless and the error stays intentionally ignored.
		delCtx, delCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer delCancel()
		policy := metav1.DeletePropagationBackground
		_ = client.BatchV1().Jobs(opts.Namespace).Delete(delCtx, created.Name, metav1.DeleteOptions{PropagationPolicy: &policy})
	}()

	// Select the pod by the runner's OWN label (present on every cluster version),
	// not the k8s-managed job-name label that only exists on >= 1.27.
	selector := "batch.kubernetes.io/job-name=" + created.Name
	if id := created.Labels[probeRunLabelKey]; id != "" {
		selector = probeRunLabelKey + "=" + id
	}
	results, err = waitAndReadProbeJob(runCtx, client, opts.Namespace, selector, grantedBudget)
	if err != nil {
		return nil, fallbackCommand, err
	}
	return results, fallbackCommand, nil
}

func canI(ctx context.Context, client kubernetes.Interface, namespace string, c capabilityCheck) (bool, error) {
	ssar := &authzv1.SelfSubjectAccessReview{
		Spec: authzv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authzv1.ResourceAttributes{
				Namespace:   namespace,
				Verb:        c.verb,
				Group:       c.group,
				Resource:    c.resource,
				Subresource: c.subresource,
			},
		},
	}
	res, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, ssar, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return res.Status.Allowed, nil
}

// probeRunLabelKey is the runner's own per-run label. Unlike the k8s-managed
// batch.kubernetes.io/job-name label (added only on clusters >= 1.27) it is
// guaranteed present, so the pod wait selector is version-independent.
const probeRunLabelKey = "radar.skyhook.io/probe-run"

// buildProbeJob is the security core: a restricted-PSA, self-destructing Job that
// runs `radar probe` once. Pure (no I/O) so it is unit-tested.
func buildProbeJob(opts RunOptions) *batchv1.Job {
	id := probeRunID()
	cmd := []string{"/radar", "probe", "--target", opts.Target, "--json", "--timeout", "3s"}
	if opts.Scheme != "" {
		cmd = append(cmd, "--scheme", opts.Scheme)
	}
	if opts.Host != "" {
		cmd = append(cmd, "--host", opts.Host)
	}
	if opts.Path != "" {
		cmd = append(cmd, "--path", opts.Path)
	}
	if opts.Layers != "" {
		cmd = append(cmd, "--layers", opts.Layers)
	}

	nonRoot := true
	noEscalate := false
	readOnlyRoot := true
	uid := int64(65532)
	automount := false
	backoff := int32(0)
	deadline := int64(25)
	ttl := int32(60)
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "radar",
		probeRunLabelKey:               id,
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "radar-probe-" + id, Namespace: opts.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automount,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &nonRoot,
						RunAsUser:      &uid,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:    "probe",
						Image:   opts.Image,
						Command: cmd,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &noEscalate,
							ReadOnlyRootFilesystem:   &readOnlyRoot,
							RunAsNonRoot:             &nonRoot,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
						},
					}},
				},
			},
		},
	}
}

// waitAndReadProbeJob polls the Job's pod to completion (or ctx deadline) and
// returns the parsed probe results from its logs. selector is the runner's own
// guaranteed label (radar.skyhook.io/probe-run=<id>), not the k8s-managed
// job-name label that only exists on clusters >= 1.27. grantedBudget > 0 means
// the run started with less than jobTimeout of request budget - an expiry then
// names the budget, not the cluster.
func waitAndReadProbeJob(ctx context.Context, client kubernetes.Interface, namespace, selector string, grantedBudget time.Duration) ([]probe.Result, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastPod *corev1.Pod
	// The most informative snapshot seen, not just the last: a pull-failing pod
	// oscillates ErrImagePull → ImagePullBackOff → ContainerCreating, and a
	// timeout that samples only the final state reports the uninformative
	// "ContainerCreating" while the actionable "image not found" was visible
	// two ticks earlier.
	var pullFailedPod *corev1.Pod
	var lastListErr error
	for {
		select {
		case <-ctx.Done():
			// Only a deadline expiry is a budget story; an explicit cancel
			// (caller went away) keeps the generic timeout attribution.
			budget := grantedBudget
			if ctx.Err() != context.DeadlineExceeded {
				budget = 0
			}
			attributed := lastPod
			if pullFailedPod != nil {
				attributed = pullFailedPod
			}
			return nil, probeTimeoutError(attributed, lastListErr, budget)
		case <-ticker.C:
			pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				// An RBAC denial will never resolve by polling - surface it now
				// with the cause instead of spinning to a confusing timeout.
				// Wrap with %w so apierrors.IsForbidden classifies it at the handler.
				if apierrors.IsForbidden(err) {
					return nil, fmt.Errorf("can't read the probe pod (RBAC denied) - run the command below: %w", err)
				}
				// Keep the real error so a never-saw-a-pod timeout can name the
				// transient read failure instead of asserting a cluster-config cause.
				lastListErr = err
				continue
			}
			if len(pods.Items) == 0 {
				continue
			}
			pod := pods.Items[0]
			lastPod = &pod
			if probeImagePullReason(&pod) != "" {
				pullFailedPod = &pod
			}
			// Gate on the probe CONTAINER's own termination, not the overall pod
			// phase: with a classic injected sidecar that runs forever, a
			// RestartPolicy:Never pod never reaches Succeeded after the probe
			// container exits, so a phase check would always time out in mesh
			// namespaces.
			if term := probeContainerTerminated(&pod); term != nil {
				if term.ExitCode == 0 {
					return readProbeResults(ctx, client, namespace, pod.Name)
				}
				raw, _ := readPodLogs(ctx, client, namespace, pod.Name)
				return nil, probeFailedError(raw, term)
			}
			if pod.Status.Phase == corev1.PodFailed {
				raw, _ := readPodLogs(ctx, client, namespace, pod.Name)
				return nil, probeFailedError(raw, probeContainerTerminated(&pod))
			}
		}
	}
}

// probeContainerTerminated returns the "probe" container's terminated state, or
// nil if it hasn't terminated. Identifying the container by name keeps the
// completion signal correct when an admission webhook injects sidecars.
func probeContainerTerminated(pod *corev1.Pod) *corev1.ContainerStateTerminated {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == "probe" {
			return pod.Status.ContainerStatuses[i].State.Terminated
		}
	}
	return nil
}

// probeFailedError builds the failure message, always carrying a cause: the
// probe container's logs when present, else its terminated reason/exitCode so a
// log-read failure never produces an empty "probe pod failed:".
func probeFailedError(raw string, term *corev1.ContainerStateTerminated) error {
	if msg := strings.TrimSpace(truncate(raw, 240)); msg != "" {
		return fmt.Errorf("in-cluster probe pod failed: %s", msg)
	}
	if term != nil {
		if term.Reason != "" {
			return fmt.Errorf("in-cluster probe pod failed: %s (exit code %d)", term.Reason, term.ExitCode)
		}
		return fmt.Errorf("in-cluster probe pod failed with exit code %d", term.ExitCode)
	}
	return fmt.Errorf("in-cluster probe pod failed before producing any output")
}

// probeTimeoutError explains WHY the probe ran out of time. The bare "didn't
// finish" is useless mid-incident; when the pod never ran its probe container the
// cause is almost always one of three generic startup blocks, so name it.
// grantedBudget > 0 means the run was capped below jobTimeout by the caller's
// remaining request budget - the expiry is then the budget's fault, and saying
// "the pod was slow" would send the operator chasing a cluster problem that
// doesn't exist.
func probeTimeoutError(pod *corev1.Pod, listErr error, grantedBudget time.Duration) error {
	const tail = " - try again, or run the command below"
	// The probe container's own image-pull failure is probe INFRASTRUCTURE
	// failing, not a timeout and never a result about the target - classify it as
	// couldn't-run and name the remedy. The usual cause: radar's image lives in a
	// private registry, and the probe Job runs in the TARGET namespace under the
	// default ServiceAccount, where radar's imagePullSecrets don't apply
	// (pull secrets are namespace-local, so they can't travel with the Job).
	// This outranks the budget framing: a pull failure would also have doomed a
	// full-length run.
	if reason := probeImagePullReason(pod); reason != "" {
		return fmt.Errorf("in-cluster test couldn't run - probe infrastructure failed to pull its image (%s), so the target was NOT tested. If the Radar image needs registry credentials, they don't apply in the target namespace (pull secrets are namespace-local) - set --reachability-image to an image that namespace can pull", reason)
	}
	if grantedBudget > 0 {
		detail := ""
		if pod != nil {
			if reason := podStartupBlock(pod); reason != "" {
				detail = " (" + reason + ")"
			} else {
				detail = fmt.Sprintf(" (the probe pod was %s)", strings.ToLower(string(pod.Status.Phase)))
			}
		}
		return fmt.Errorf("ran out of request time budget (%ds granted) before the probe finished%s - re-run to test the remaining routes", int(grantedBudget.Round(time.Second).Seconds()), detail)
	}
	if pod == nil {
		// We only know we never observed the pod run. When a pod LIST failed, name
		// that real error rather than asserting a cluster-config cause we can't
		// confirm. Otherwise name the usual suspects honestly without overclaiming.
		if listErr != nil {
			return fmt.Errorf("in-cluster probe timed out: Radar couldn't read the probe pod (%v)%s", listErr, tail)
		}
		return fmt.Errorf("in-cluster probe timed out: Radar never saw the probe pod run - it may have been blocked by an admission webhook, denied by quota, or left unschedulable%s", tail)
	}
	if reason := podStartupBlock(pod); reason != "" {
		return fmt.Errorf("in-cluster probe timed out: %s%s", reason, tail)
	}
	return fmt.Errorf("in-cluster probe timed out while the pod was %s%s", strings.ToLower(string(pod.Status.Phase)), tail)
}

// probeImagePullReason returns the probe container's image-pull waiting reason,
// or "" when it isn't blocked on one. Scoped to the "probe" container by name -
// an injected sidecar's pull failure is the sidecar's fault and stays on the
// generic podStartupBlock path.
func probeImagePullReason(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if cs.Name != "probe" || cs.State.Waiting == nil {
			continue
		}
		if k8s.IsImagePullReason(cs.State.Waiting.Reason) {
			// The kubelet's message names the actual failure ("… not found",
			// "unauthorized") - the reason alone sends the operator guessing.
			if msg := strings.TrimSpace(cs.State.Waiting.Message); msg != "" {
				return cs.State.Waiting.Reason + ": " + truncate(msg, 200)
			}
			return cs.State.Waiting.Reason
		}
	}
	return ""
}

// podStartupBlock names the generic reason a pod hasn't run its main container yet
// - unschedulable, injected init containers still running (our probe Job declares
// none, so any are from an admission webhook), or image pull. Returns "" when
// nothing specific stands out.
func podStartupBlock(pod *corev1.Pod) string {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue {
			if c.Message != "" {
				return "the probe pod couldn't be scheduled (" + c.Message + ")"
			}
			return "the probe pod couldn't be scheduled onto any node"
		}
	}
	if total := len(pod.Status.InitContainerStatuses); total > 0 {
		done := 0
		for _, cs := range pod.Status.InitContainerStatuses {
			if cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0 {
				done++
			}
		}
		if done < total {
			// Certain by construction: our Job template declares ZERO init
			// containers, so any present were added by a mutating admission
			// webhook. Said with confidence because there is no other source.
			return fmt.Sprintf("the probe container couldn't start - an admission webhook injected %d init container(s) into the probe pod and they hadn't finished", total-done)
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting == nil || cs.State.Waiting.Reason == "" {
			continue
		}
		// Attribute by container name, consistent with probeContainerTerminated /
		// readPodLogs. A waiting NON-probe container is an injected sidecar - its
		// ImagePullBackOff is not the probe's fault, so never blame the probe.
		if cs.Name == "probe" {
			return "the probe container couldn't start (" + cs.State.Waiting.Reason + ")"
		}
		return fmt.Sprintf("a sidecar container (%s) couldn't start (%s)", cs.Name, cs.State.Waiting.Reason)
	}
	return ""
}

func readProbeResults(ctx context.Context, client kubernetes.Interface, namespace, podName string) ([]probe.Result, error) {
	raw, err := readPodLogs(ctx, client, namespace, podName)
	if err != nil {
		return nil, fmt.Errorf("couldn't read probe results: %w", err)
	}
	return parseProbeOutput(raw)
}

// parseProbeOutput parses the `radar probe --json` stdout into probe results and
// stamps the in-cluster vantage (these were observed from inside the dataplane).
func parseProbeOutput(raw string) ([]probe.Result, error) {
	var results []probe.Result
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		return nil, fmt.Errorf("probe output wasn't valid JSON: %s", strings.TrimSpace(truncate(raw, 240)))
	}
	for i := range results {
		results[i].Vantage = probe.VantageInCluster
	}
	return results, nil
}

func readPodLogs(ctx context.Context, client kubernetes.Interface, namespace, podName string) (string, error) {
	// Name the container explicitly: in a mesh/admission namespace the probe pod
	// gets injected sidecars, and pods/log requires a container name once a pod has
	// more than one. The Job always names its container "probe".
	stream, err := client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: "probe"}).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	b, err := io.ReadAll(io.LimitReader(stream, 1<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// FallbackCommand is the copyable kubectl the caller runs themselves when they
// can't create the Job. It MUST mirror the exact args the Job uses
// (target/scheme/host/path/layers) so the manual run tests the same request the
// runner would have - a fallback that tested a different request would mislead.
func FallbackCommand(opts RunOptions) string {
	image := opts.Image
	if image == "" {
		image = DefaultImage()
	}
	// Mirror the Job's restricted security envelope so the manual run tests the
	// SAME thing the runner would - and survives a restricted-PSA namespace, where
	// a bare `kubectl run` is admission-rejected. Strategic merge keeps the
	// container command (supplied after `--`) while layering on the securityContext
	// + automountServiceAccountToken=false the probe Job declares.
	overrides := map[string]any{
		"spec": map[string]any{
			"automountServiceAccountToken": false,
			"securityContext": map[string]any{
				"runAsNonRoot":   true,
				"runAsUser":      65532,
				"seccompProfile": map[string]any{"type": "RuntimeDefault"},
			},
			"containers": []any{map[string]any{
				"name": "radar-probe-manual",
				"securityContext": map[string]any{
					"allowPrivilegeEscalation": false,
					"readOnlyRootFilesystem":   true,
					"runAsNonRoot":             true,
					"capabilities":             map[string]any{"drop": []any{"ALL"}},
				},
			}},
		},
	}
	ov, _ := json.Marshal(overrides)
	// Only the user-controlled VALUES are shell-quoted (a crafted host/path can't
	// break out); the fixed kubectl flags stay bare so the command reads cleanly.
	parts := []string{
		"kubectl run radar-probe-manual -n " + shellQuote(opts.Namespace),
		"--image=" + shellQuote(image), "--restart=Never --rm -i",
		"--override-type=strategic --overrides=" + shellQuote(string(ov)),
		"--command --",
		"/radar probe --target " + shellQuote(opts.Target), "--timeout 3s",
	}
	if opts.Scheme != "" {
		parts = append(parts, "--scheme "+shellQuote(opts.Scheme))
	}
	if opts.Host != "" {
		parts = append(parts, "--host "+shellQuote(opts.Host))
	}
	if opts.Path != "" {
		parts = append(parts, "--path "+shellQuote(opts.Path))
	}
	if opts.Layers != "" {
		parts = append(parts, "--layers "+shellQuote(opts.Layers))
	}
	parts = append(parts, "--json")
	return strings.Join(parts, " ")
}

func probeRunID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// shellQuote wraps a value in single quotes, escaping any embedded single quote,
// so a crafted host/path/namespace can't break out of the fallback command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
