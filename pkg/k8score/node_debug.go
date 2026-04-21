package k8score

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// NodeDebugPodResult contains the coordinates of a created debug pod.
type NodeDebugPodResult struct {
	PodName       string `json:"podName"`
	Namespace     string `json:"namespace"`
	ContainerName string `json:"containerName"`
	NodeName      string `json:"nodeName"`
}

var nodeNameSanitizer = regexp.MustCompile(`[^a-z0-9-]`)
var collapseDashes = regexp.MustCompile(`-{2,}`)
var trimDashes = regexp.MustCompile(`^-+|-+$`)

// sanitizeNodeName replaces characters not allowed in pod names with dashes.
func sanitizeNodeName(name string) string {
	s := nodeNameSanitizer.ReplaceAllString(name, "-")
	s = collapseDashes.ReplaceAllString(s, "-")
	s = trimDashes.ReplaceAllString(s, "")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// sanitizeLabelValue ensures a string is valid as a Kubernetes label value
// (max 63 chars, alphanumeric start/end, interior allows [a-zA-Z0-9._-]).
func sanitizeLabelValue(v string) string {
	s := nodeNameSanitizer.ReplaceAllString(v, "-")
	s = collapseDashes.ReplaceAllString(s, "-")
	s = trimDashes.ReplaceAllString(s, "")
	if len(s) > 63 {
		s = s[:63]
	}
	s = trimDashes.ReplaceAllString(s, "")
	return s
}

// CreateNodeDebugPod creates a privileged debug pod scheduled on the given node.
// The pod runs with host PID/network/IPC and mounts the host root at /host.
func CreateNodeDebugPod(ctx context.Context, client kubernetes.Interface, nodeName, image string) (*NodeDebugPodResult, error) {
	if client == nil {
		return nil, fmt.Errorf("kubernetes client not initialized")
	}

	if image == "" {
		image = "busybox:latest"
	}

	containerName := "debug"
	sanitized := sanitizeNodeName(nodeName)
	labelValue := sanitizeLabelValue(nodeName)
	podName := fmt.Sprintf("radar-node-debug-%s-%d", sanitized, time.Now().Unix())

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "default",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "radar",
				"radarhq.io/debug-node":        labelValue,
			},
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			HostPID:       true,
			HostNetwork:   true,
			HostIPC:       true,
			RestartPolicy:       corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: func() *int64 { d := int64(3600); return &d }(),
			Containers: []corev1.Container{{
				Name:    containerName,
				Image:   image,
				Command: []string{"sleep", "infinity"},
				Stdin:   true,
				TTY:     true,
				SecurityContext: &corev1.SecurityContext{
					Privileged: func() *bool { b := true; return &b }(),
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "host-root",
					MountPath: "/host",
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "host-root",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/",
					},
				},
			}},
			Tolerations: []corev1.Toleration{{
				Operator: corev1.TolerationOpExists,
			}},
		},
	}

	created, err := client.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create debug pod on node %s: %w", nodeName, err)
	}

	return &NodeDebugPodResult{
		PodName:       created.Name,
		Namespace:     created.Namespace,
		ContainerName: containerName,
		NodeName:      nodeName,
	}, nil
}

// WaitForPodRunning polls until the named pod reaches Running phase or the timeout expires.
func WaitForPodRunning(ctx context.Context, client kubernetes.Interface, namespace, podName string, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timed out waiting for pod %s/%s to reach Running state", namespace, podName)
		case <-ticker.C:
			pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to get pod %s/%s: %w", namespace, podName, err)
			}
			if pod.Status.Phase == corev1.PodRunning {
				return nil
			}
			if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
				return fmt.Errorf("pod %s/%s entered %s phase", namespace, podName, pod.Status.Phase)
			}
		}
	}
}

// DeleteNodeDebugPods deletes all debug pods for the given node. Matches both
// the current "radarhq.io/debug-node" label and the legacy "radar.skyhook.io/
// debug-node" label so a Radar binary upgraded mid-debug-session can still
// GC in-flight privileged pods created by the prior version.
//
// TODO(2026-Q3): drop the legacy selector once the migration window is done
// (paired with the Argo legacy annotations in pkg/gitops/operations.go).
func DeleteNodeDebugPods(ctx context.Context, client kubernetes.Interface, nodeName string) error {
	if client == nil {
		return fmt.Errorf("kubernetes client not initialized")
	}

	gracePeriod := int64(0)
	selectors := []string{
		fmt.Sprintf("radarhq.io/debug-node=%s", sanitizeLabelValue(nodeName)),
		fmt.Sprintf("radar.skyhook.io/debug-node=%s", sanitizeLabelValue(nodeName)),
	}
	var errs []error
	for _, sel := range selectors {
		if err := client.CoreV1().Pods("default").DeleteCollection(ctx,
			metav1.DeleteOptions{GracePeriodSeconds: &gracePeriod},
			metav1.ListOptions{LabelSelector: sel},
		); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

