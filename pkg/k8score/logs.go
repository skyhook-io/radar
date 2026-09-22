package k8score

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// LogOptions configures log fetching behavior.
type LogOptions struct {
	TailLines    *int64
	LimitBytes   *int64
	SinceSeconds *int64
	Previous     bool
	Timestamps   bool
	Follow       bool
}

// GetContainerLogs returns a stream of logs for a container.
// The caller is responsible for closing the returned ReadCloser.
func GetContainerLogs(ctx context.Context, client kubernetes.Interface, namespace, podName, containerName string, opts LogOptions) (io.ReadCloser, error) {
	if client == nil {
		return nil, fmt.Errorf("kubernetes client not initialized")
	}
	podLogOpts := &corev1.PodLogOptions{
		Container:    containerName,
		TailLines:    opts.TailLines,
		LimitBytes:   opts.LimitBytes,
		SinceSeconds: opts.SinceSeconds,
		Previous:     opts.Previous,
		Timestamps:   opts.Timestamps,
		Follow:       opts.Follow,
	}

	req := client.CoreV1().Pods(namespace).GetLogs(podName, podLogOpts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to stream logs for %s/%s/%s: %w", namespace, podName, containerName, err)
	}

	return stream, nil
}
