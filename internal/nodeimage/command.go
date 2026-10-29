package nodeimage

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/pkg/k8score"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"
)

// Run dispatches the experimental node-image command. It never starts Radar's server.
func Run(args []string, stdout, stderr io.Writer) int {
	if err := run(args, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("use node-image inspect, serve, or blob")
	}
	f := flag.NewFlagSet("node-image "+args[0], flag.ContinueOnError)
	f.SetOutput(stderr)
	root := f.String("root", "/content", "Read-only containerd content directory")
	digest := f.String("digest", "", "SHA-256 digest of the content blob")
	kubeconfig := f.String("kubeconfig", "", "Kubeconfig for this inspection")
	contextName := f.String("context", "", "Kubernetes context")
	namespace := f.String("namespace", "default", "Workload and helper namespace")
	podName := f.String("pod", "", "Workload Pod to inspect")
	container := f.String("container", "", "Container name; required for Pods with multiple containers")
	helperImage := f.String("helper-image", "", "Image containing this Radar binary")
	store := f.String("store", "/var/lib/containerd/io.containerd.content.v1.content", "Node content directory; k3s uses /var/lib/rancher/k3s/agent/containerd/io.containerd.content.v1.content")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "blob":
		return CopyBlob(*root, *digest, stdout)
	case "serve":
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(3 * time.Minute):
			return nil
		}
	case "inspect":
	default:
		return fmt.Errorf("unknown node-image command %q", args[0])
	}
	if *podName == "" || *helperImage == "" {
		return fmt.Errorf("inspect requires --pod and --helper-image")
	}
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = *kubeconfig
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: *contextName}).ClientConfig()
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	// This CLI uses the caller's Kubernetes credentials throughout. It has no
	// shared server identity and cannot grant a caller additional cluster access.
	pod, err := client.CoreV1().Pods(*namespace).Get(ctx, *podName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	imageRef, imageDigest, err := workloadImage(pod, *container)
	if err != nil {
		return err
	}
	review, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{Namespace: pod.Namespace, Verb: "create", Resource: "pods", Subresource: "exec", Name: pod.Name}}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("could not check permission to exec into the workload: %w", err)
	}
	if !review.Status.Allowed {
		return fmt.Errorf("node image inspection requires permission to exec into the workload Pod")
	}
	node, err := client.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read node platform: %w", err)
	}
	platform := node.Status.NodeInfo.OperatingSystem + "/" + node.Status.NodeInfo.Architecture
	helper := helperPod(pod, *helperImage, *store)
	created, err := client.CoreV1().Pods(*namespace).Create(ctx, helper, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("node inspection is unavailable: helper Pod was rejected: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := client.CoreV1().Pods(*namespace).Delete(cleanup, created.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0)), Preconditions: &metav1.Preconditions{UID: &created.UID}})
		if err != nil {
			fmt.Fprintf(stderr, "Helper cleanup failed for %s/%s: %v\n", *namespace, created.Name, err)
		}
	}()
	fmt.Fprintf(stderr, "Reading %s on node %s through helper %s\n", imageDigest, pod.Spec.NodeName, created.Name)
	if err := k8score.WaitForPodRunning(ctx, client, *namespace, created.Name, 60*time.Second); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "radar-node-content-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	img, err := loadImage(ctx, dir, imageDigest, platform, func(ctx context.Context, digest string, dst io.Writer) error {
		executor, err := k8score.NewPodCommandExecutor(client, cfg, *namespace, created.Name, "reader", []string{"/radar", "node-image", "blob", "--digest", digest})
		if err != nil {
			return err
		}
		var diagnostic bytes.Buffer
		err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: dst, Stderr: &diagnostic})
		if err != nil {
			return fmt.Errorf("read node image content: %w: %s", err, diagnostic.String())
		}
		return nil
	})
	if err != nil {
		return err
	}
	fs, err := images.InspectLocal(ctx, img, imageRef)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(struct {
		Source     string                  `json:"source"`
		Node       string                  `json:"node"`
		Filesystem *images.ImageFilesystem `json:"filesystem"`
	}{"Node runtime", pod.Spec.NodeName, fs})
}

func workloadImage(pod *corev1.Pod, container string) (string, string, error) {
	if pod.Spec.NodeName == "" {
		return "", "", fmt.Errorf("the Pod has not been assigned to a node")
	}
	if container == "" {
		if len(pod.Spec.Containers) != 1 {
			return "", "", fmt.Errorf("choose a container with --container")
		}
		container = pod.Spec.Containers[0].Name
	}
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses, pod.Status.EphemeralContainerStatuses} {
		for _, s := range statuses {
			if s.Name != container {
				continue
			}
			id := s.ImageID
			if _, after, ok := strings.Cut(id, "://"); ok {
				id = after
			}
			if _, after, ok := strings.Cut(id, "@"); ok {
				id = after
			}
			if !digestPattern.MatchString(id) {
				return "", "", fmt.Errorf("the container has no supported pulled image digest")
			}
			return s.Image, id, nil
		}
	}
	return "", "", fmt.Errorf("the container has no kubelet image status yet")
}

func helperPod(workload *corev1.Pod, image, store string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "radar-image-reader-", Namespace: workload.Namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "radar", "radarhq.io/image-reader": "true"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: workload.Name, UID: workload.UID}}},
		Spec: corev1.PodSpec{
			NodeName: workload.Spec.NodeName, RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false), ActiveDeadlineSeconds: ptr.To(int64(180)), TerminationGracePeriodSeconds: ptr.To(int64(1)),
			Tolerations:     []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr.To(int64(0)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "reader", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/radar", "node-image", "serve"},
				SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(false), AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("128Mi")}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "content", MountPath: "/content", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "content", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: store, Type: ptr.To(corev1.HostPathDirectory)}}}},
		},
	}
}
