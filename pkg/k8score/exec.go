package k8score

import (
	"fmt"

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
	if client == nil {
		return nil, fmt.Errorf("kubernetes client not initialized")
	}
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdin:     true,
			Stdout:    true,
			// When TTY is true, the terminal muxes stderr into stdout, so Stderr must be false.
			// Setting both TTY and Stderr causes stream errors on some API servers; matches kubectl exec -it.
			Stderr: !tty,
			TTY:    tty,
		}, scheme.ParameterCodec)

	executor, err := rcpkg.NewExecutor(config, req.URL())
	if err != nil {
		return nil, fmt.Errorf("failed to create exec executor for %s/%s/%s: %w", namespace, podName, containerName, err)
	}

	return executor, nil
}
