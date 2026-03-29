package k8s

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// Re-export types from pkg/k8score for backward compatibility.
type EphemeralContainerOptions = k8score.EphemeralContainerOptions

const DefaultDebugImage = k8score.DefaultDebugImage

// CreateEphemeralContainer adds an ephemeral debug container to a pod.
func CreateEphemeralContainer(ctx context.Context, opts EphemeralContainerOptions) (*corev1.EphemeralContainer, error) {
	return k8score.CreateEphemeralContainer(ctx, GetClient(), opts)
}

// CreateEphemeralContainerWithClient adds an ephemeral debug container using the given client.
// If client is nil, uses the shared client.
func CreateEphemeralContainerWithClient(ctx context.Context, opts EphemeralContainerOptions, client kubernetes.Interface) (*corev1.EphemeralContainer, error) {
	if client == nil {
		client = GetClient()
	}
	return k8score.CreateEphemeralContainer(ctx, client, opts)
}

// WaitForEphemeralContainer polls until an ephemeral container reaches Running state or timeout.
func WaitForEphemeralContainer(ctx context.Context, namespace, podName, containerName string, timeout time.Duration) error {
	return k8score.WaitForEphemeralContainer(ctx, GetClient(), namespace, podName, containerName, timeout)
}
