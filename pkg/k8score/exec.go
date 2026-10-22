package k8score

import (
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	rcpkg "github.com/skyhook-io/radar/pkg/remotecommand"
)

// NewPodExecExecutor creates an executor for running commands in a pod container.
// Tries WebSocket first (k8s ≥1.29); falls back to SPDY on upgrade failure for older clusters.
// The caller uses the returned Executor to call StreamWithContext.
func NewPodExecExecutor(client kubernetes.Interface, config *rest.Config, namespace, podName, containerName string, command []string, tty bool) (remotecommand.Executor, error) {
	return newPodExecutor(client, config, namespace, podName, &corev1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdin:     true,
		Stdout:    true,
		// When TTY is true, the terminal muxes stderr into stdout, so Stderr must be false.
		// Setting both TTY and Stderr causes stream errors on some API servers; matches kubectl exec -it.
		Stderr: !tty,
		TTY:    tty,
	})
}

// NewPodCommandExecutor creates an executor for a non-interactive command:
// no stdin and no TTY, with stdout and stderr kept apart, like
// `kubectl exec pod -- cmd`. Without stdin the command sees EOF at once
// instead of waiting on a stream nobody writes to.
func NewPodCommandExecutor(client kubernetes.Interface, config *rest.Config, namespace, podName, containerName string, command []string) (remotecommand.Executor, error) {
	return newPodExecutor(client, config, namespace, podName, &corev1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdout:    true,
		Stderr:    true,
	})
}

func newPodExecutor(client kubernetes.Interface, config *rest.Config, namespace, podName string, opts *corev1.PodExecOptions) (remotecommand.Executor, error) {
	if client == nil {
		return nil, fmt.Errorf("kubernetes client not initialized")
	}
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(opts, scheme.ParameterCodec)

	executor, err := rcpkg.NewExecutor(config, req.URL())
	if err != nil {
		return nil, fmt.Errorf("failed to create exec executor for %s/%s/%s: %w", namespace, podName, opts.Container, err)
	}

	return executor, nil
}

// DefaultContainerAnnotation is the client-side convention kubectl, k9s, and
// Lens use to mark the "main" container of a multi-container pod. Service
// meshes (Istio, Linkerd, ASM) set it during injection to point past their
// sidecar. Clients that simply default to containers[0] can land in a
// distroless mesh proxy with no shell.
const DefaultContainerAnnotation = "kubectl.kubernetes.io/default-container"

// DefaultExecContainer picks the container to target when the request doesn't
// name one. It honors DefaultContainerAnnotation (matching kubectl exec
// behavior). Falls back to the first non-mesh app container; returns "" when none is available.
func DefaultExecContainer(pod *corev1.Pod) string {
	if name := pod.Annotations[DefaultContainerAnnotation]; name != "" && slices.Contains(ExecContainerNames(pod), name) {
		return name
	}

	for _, c := range pod.Spec.Containers {
		if MeshProxyContainers[c.Name] == "" {
			return c.Name
		}
	}
	return ""
}

// MeshProxyContainers identifies injected infrastructure proxies.
var MeshProxyContainers = map[string]string{
	"istio-proxy":      "Istio",
	"linkerd-proxy":    "Linkerd",
	"consul-dataplane": "Consul",
}

// ExecContainerNames includes regular, init and ephemeral containers.
// The API server enforces whether a selected container is running.
func ExecContainerNames(pod *corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers)+len(pod.Spec.EphemeralContainers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name)
	}
	for _, c := range pod.Spec.EphemeralContainers {
		names = append(names, c.Name)
	}
	return names
}
