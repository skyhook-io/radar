package k8s

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// ClusterInfo contains detected cluster information
type ClusterInfo struct {
	Context           string `json:"context"`           // kubeconfig context name
	Cluster           string `json:"cluster"`           // cluster name from kubeconfig
	Platform          string `json:"platform"`          // gke, gke-autopilot, eks, aks, minikube, kind, docker-desktop, generic
	KubernetesVersion string `json:"kubernetesVersion"`
	NodeCount         int    `json:"nodeCount"`
	PodCount          int    `json:"podCount"`
	NamespaceCount    int    `json:"namespaceCount"`
}

// GetClusterInfo returns detected cluster information
func GetClusterInfo(ctx context.Context) (*ClusterInfo, error) {
	platform, _ := GetClusterPlatform(ctx)

	info := &ClusterInfo{
		Context:  GetContextName(),
		Cluster:  GetClusterName(),
		Platform: platform,
	}

	// Get version info
	if k8sClient != nil {
		if version, err := k8sClient.Discovery().ServerVersion(); err == nil {
			info.KubernetesVersion = version.GitVersion
		}
	}

	// Get counts from cache
	cache := GetResourceCache()
	if cache != nil {
		if nodes, err := cache.Nodes().List(labels.Everything()); err == nil {
			info.NodeCount = len(nodes)
		}
		if pods, err := cache.Pods().List(labels.Everything()); err == nil {
			info.PodCount = len(pods)
		}
		if namespaces, err := cache.Namespaces().List(labels.Everything()); err == nil {
			info.NamespaceCount = len(namespaces)
		}
	}

	return info, nil
}

// GetClusterPlatform attempts to detect the Kubernetes platform/provider
func GetClusterPlatform(ctx context.Context) (string, error) {
	var nodes []corev1.Node
	cache := GetResourceCache()
	if cache != nil {
		nodeList, err := cache.Nodes().List(labels.Everything())
		if err == nil && len(nodeList) > 0 {
			for _, n := range nodeList {
				nodes = append(nodes, *n)
			}
		}
	}

	// Fallback to direct API if cache unavailable
	if len(nodes) == 0 && k8sClient != nil {
		nodeList, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{
			Limit: 1,
		})
		if err != nil {
			return detectPlatformFallback(ctx)
		}
		if len(nodeList.Items) == 0 {
			return "unknown", nil
		}
		nodes = nodeList.Items
	}

	if len(nodes) == 0 {
		return "unknown", nil
	}

	node := nodes[0]

	// Primary detection: Provider ID
	platform := detectByProviderID(node)
	if platform != "unknown" {
		if platform == "gke" {
			if isAutopilot, _ := IsGKEAutopilot(ctx); isAutopilot {
				return "gke-autopilot", nil
			}
		}
		return platform, nil
	}

	// Secondary detection: Platform-specific labels
	platform = detectByLabels(node)
	if platform != "unknown" {
		if platform == "gke" {
			if isAutopilot, _ := IsGKEAutopilot(ctx); isAutopilot {
				return "gke-autopilot", nil
			}
		}
		return platform, nil
	}

	// Tertiary detection: Node name patterns
	platform = detectByNodeName(node)
	if platform != "unknown" {
		return platform, nil
	}

	return "generic", nil
}

// IsGKEAutopilot detects if the cluster is GKE Autopilot
func IsGKEAutopilot(ctx context.Context) (bool, error) {
	var nodes []corev1.Node
	cache := GetResourceCache()
	if cache != nil {
		nodeList, err := cache.Nodes().List(labels.Everything())
		if err == nil && len(nodeList) > 0 {
			for _, n := range nodeList {
				nodes = append(nodes, *n)
			}
		}
	}

	if len(nodes) == 0 && k8sClient != nil {
		nodeList, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
		if err == nil && len(nodeList.Items) > 0 {
			nodes = nodeList.Items
		}
	}

	if len(nodes) > 0 {
		node := nodes[0]
		if val, exists := node.Labels["cloud.google.com/gke-autopilot"]; exists && val == "true" {
			return true, nil
		}
		if !isNodeGKE(node) {
			return false, nil
		}
	}

	// Check pod annotations for Autopilot
	isAutopilot, found := checkAutopilotViaAnnotations(ctx)
	if found {
		return isAutopilot, nil
	}

	return false, nil
}

func checkAutopilotViaAnnotations(ctx context.Context) (bool, bool) {
	var pods []corev1.Pod
	cache := GetResourceCache()
	if cache != nil {
		podList, err := cache.Pods().Pods("kube-system").List(labels.Everything())
		if err == nil && len(podList) > 0 {
			for i, pod := range podList {
				pods = append(pods, *pod)
				if i >= 9 {
					break
				}
			}
		}
	}

	if len(pods) == 0 && k8sClient != nil {
		podList, err := k8sClient.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{Limit: 10})
		if err == nil {
			pods = podList.Items
		}
	}

	for _, pod := range pods {
		for key := range pod.Annotations {
			if strings.HasPrefix(key, "autopilot.gke.io/") {
				return true, true
			}
		}
	}

	return false, len(pods) > 0
}

func detectByProviderID(node corev1.Node) string {
	providerID := node.Spec.ProviderID

	if strings.HasPrefix(providerID, "gce://") || strings.HasPrefix(providerID, "gke://") {
		return "gke"
	}
	if strings.HasPrefix(providerID, "aws://") {
		return "eks"
	}
	if strings.HasPrefix(providerID, "azure://") {
		return "aks"
	}

	return "unknown"
}

func detectByLabels(node corev1.Node) string {
	if isNodeGKE(node) {
		return "gke"
	}

	if _, exists := node.Labels["eks.amazonaws.com/nodegroup"]; exists {
		return "eks"
	}
	if _, exists := node.Labels["eks.amazonaws.com/capacityType"]; exists {
		return "eks"
	}

	for label := range node.Labels {
		if strings.HasPrefix(label, "kubernetes.azure.com/") {
			return "aks"
		}
	}

	if _, exists := node.Labels["node.openshift.io/os_id"]; exists {
		return "openshift"
	}

	if _, exists := node.Labels["rke.cattle.io/machine"]; exists {
		return "rancher"
	}

	return "unknown"
}

func detectByNodeName(node corev1.Node) string {
	name := node.Name

	if strings.Contains(name, "kind-") {
		return "kind"
	}
	if name == "minikube" || strings.HasPrefix(name, "minikube-") {
		return "minikube"
	}
	if name == "docker-desktop" {
		return "docker-desktop"
	}

	return "unknown"
}

func isNodeGKE(node corev1.Node) bool {
	if _, exists := node.Labels["cloud.google.com/gke-nodepool"]; exists {
		return true
	}
	if _, exists := node.Labels["cloud.google.com/gke-os-distribution"]; exists {
		return true
	}
	if strings.HasPrefix(node.Spec.ProviderID, "gce://") {
		return true
	}
	return false
}

func detectPlatformFallback(ctx context.Context) (string, error) {
	isAutopilot, found := checkAutopilotViaAnnotations(ctx)
	if found && isAutopilot {
		return "gke-autopilot", nil
	}
	return "unknown", nil
}
