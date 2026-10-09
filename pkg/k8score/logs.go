package k8score

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

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

// ErrLogsUnavailable means the node answered a log request without the
// container's output. Usually the container or its log file has already been
// removed from the node; the same answer comes back when the container runtime
// is briefly unreachable, or for a few seconds while the kubelet reopens a
// running container's log file, so this does not claim the lines are gone for
// good.
var ErrLogsUnavailable = errors.New("Kubernetes did not return this container's logs")

// IsLogsUnavailableNotice recognises the kubelet's notice that it could not
// read a container's logs. The apiserver relays it with a 200 as the whole
// response body, so a reader that does not check shows it as a line the
// container printed. Only a body that is exactly one such line matches. With
// timestamps on, a real line always starts with one, so it cannot match; with
// them off, only a container whose sole output is that sentence would.
func IsLogsUnavailableNotice(body string) bool {
	body = strings.TrimSpace(body)
	if strings.Contains(body, "\n") {
		return false
	}
	// The runtime no longer knows the container.
	if strings.HasPrefix(body, "unable to retrieve container logs for ") {
		return true
	}
	// The container's log file is missing on the node. The kubelet quotes the
	// path of the file it looked for, which is always a .log file.
	return strings.HasPrefix(body, `failed to try resolving symlinks in path "`) && strings.Contains(body, `.log": `)
}
