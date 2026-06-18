package k8score

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DefaultDebugImage is the default image for ephemeral debug containers.
const DefaultDebugImage = "busybox:latest"

const defaultDebugRunAsUser int64 = 65532

// EphemeralContainerOptions configures debug container creation.
type EphemeralContainerOptions struct {
	Namespace       string
	PodName         string
	TargetContainer string // Container to share process namespace with
	Image           string // Debug image (default: busybox:latest)
	ContainerName   string // Name for ephemeral container (auto-generated if empty)
}

// CreateEphemeralContainer adds an ephemeral debug container to a pod.
func CreateEphemeralContainer(ctx context.Context, client kubernetes.Interface, opts EphemeralContainerOptions) (*corev1.EphemeralContainer, error) {
	if client == nil {
		return nil, fmt.Errorf("kubernetes client not initialized")
	}
	if opts.Image == "" {
		opts.Image = DefaultDebugImage
	}
	if opts.ContainerName == "" {
		opts.ContainerName = fmt.Sprintf("debug-%d", time.Now().Unix())
	}

	pod, err := client.CoreV1().Pods(opts.Namespace).Get(ctx, opts.PodName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}

	ec := newEphemeralContainer(opts, nil)
	updateErr := updateEphemeralContainer(ctx, client, pod, ec)
	if updateErr != nil && isRestrictedPodSecurityError(updateErr) {
		pod, err = client.CoreV1().Pods(opts.Namespace).Get(ctx, opts.PodName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get pod: %w", err)
		}
		securityContext := debugContainerSecurityContext(pod, opts.TargetContainer)
		if securityContext != nil {
			ec = newEphemeralContainer(opts, securityContext)
			updateErr = updateEphemeralContainer(ctx, client, pod, ec)
		}
	}
	if updateErr != nil {
		return nil, fmt.Errorf("failed to create ephemeral container: %w", updateErr)
	}

	return &ec, nil
}

func newEphemeralContainer(opts EphemeralContainerOptions, securityContext *corev1.SecurityContext) corev1.EphemeralContainer {
	return corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:                     opts.ContainerName,
			Image:                    opts.Image,
			ImagePullPolicy:          corev1.PullIfNotPresent,
			Stdin:                    true,
			TTY:                      true,
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			SecurityContext:          securityContext,
		},
		TargetContainerName: opts.TargetContainer,
	}
}

func updateEphemeralContainer(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, ec corev1.EphemeralContainer) error {
	updated := pod.DeepCopy()
	updated.Spec.EphemeralContainers = append(updated.Spec.EphemeralContainers, ec)
	_, err := client.CoreV1().Pods(updated.Namespace).UpdateEphemeralContainers(
		ctx,
		updated.Name,
		updated,
		metav1.UpdateOptions{},
	)
	return err
}

func isRestrictedPodSecurityError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "violates PodSecurity") && strings.Contains(msg, "restricted")
}

func debugContainerSecurityContext(pod *corev1.Pod, targetContainer string) *corev1.SecurityContext {
	if pod != nil && pod.Spec.OS != nil && pod.Spec.OS.Name == corev1.Windows {
		return nil
	}

	runAsUser := debugRunAsUser(pod, targetContainer)
	runAsGroup := debugRunAsGroup(pod, targetContainer)
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		RunAsNonRoot: boolPtr(true),
		RunAsGroup:   runAsGroup,
		RunAsUser:    runAsUser,
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

func debugRunAsUser(pod *corev1.Pod, targetContainer string) *int64 {
	if pod != nil {
		for i := range pod.Spec.Containers {
			c := &pod.Spec.Containers[i]
			if c.Name == targetContainer && c.SecurityContext != nil && c.SecurityContext.RunAsUser != nil && *c.SecurityContext.RunAsUser != 0 {
				runAsUser := *c.SecurityContext.RunAsUser
				return &runAsUser
			}
		}
		if pod.Spec.SecurityContext != nil && pod.Spec.SecurityContext.RunAsUser != nil && *pod.Spec.SecurityContext.RunAsUser != 0 {
			runAsUser := *pod.Spec.SecurityContext.RunAsUser
			return &runAsUser
		}
	}
	runAsUser := defaultDebugRunAsUser
	return &runAsUser
}

func debugRunAsGroup(pod *corev1.Pod, targetContainer string) *int64 {
	if pod != nil {
		for i := range pod.Spec.Containers {
			c := &pod.Spec.Containers[i]
			if c.Name == targetContainer && c.SecurityContext != nil && c.SecurityContext.RunAsGroup != nil && *c.SecurityContext.RunAsGroup != 0 {
				runAsGroup := *c.SecurityContext.RunAsGroup
				return &runAsGroup
			}
		}
		if pod.Spec.SecurityContext != nil && pod.Spec.SecurityContext.RunAsGroup != nil && *pod.Spec.SecurityContext.RunAsGroup != 0 {
			runAsGroup := *pod.Spec.SecurityContext.RunAsGroup
			return &runAsGroup
		}
	}
	return nil
}

// WaitForEphemeralContainer polls until an ephemeral container reaches Running state or timeout.
func WaitForEphemeralContainer(ctx context.Context, client kubernetes.Interface, namespace, podName, containerName string, timeout time.Duration) error {
	if client == nil {
		return fmt.Errorf("kubernetes client not initialized")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get pod: %w", err)
		}

		for _, status := range pod.Status.EphemeralContainerStatuses {
			if status.Name == containerName {
				if status.State.Running != nil {
					return nil
				}
				if status.State.Terminated != nil {
					return fmt.Errorf("container terminated: %s", status.State.Terminated.Reason)
				}
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			continue
		}
	}

	return fmt.Errorf("timeout waiting for ephemeral container to start")
}
