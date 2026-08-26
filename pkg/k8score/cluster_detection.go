package k8score

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DetectClusterPlatform detects the Kubernetes platform/provider by inspecting nodes.
// Returns one of: rke2, gke, gke-autopilot, eks, aks, minikube, kind, docker-desktop, openshift, rancher, generic, unknown.
func DetectClusterPlatform(ctx context.Context, client kubernetes.Interface) (string, error) {
	if client == nil {
		return "unknown", nil
	}

	nodeList, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil || len(nodeList.Items) == 0 {
		return "unknown", err
	}

	node := nodeList.Items[0]

	platform := DetectNodePlatform(node)
	if platform != "unknown" {
		if platform == "gke" {
			if isAutopilot, _ := detectAutopilot(ctx, client); isAutopilot {
				return "gke-autopilot", nil
			}
		}
		return platform, nil
	}

	return "generic", nil
}

// DetectNodePlatform classifies a sampled node. Distribution markers take
// precedence over infrastructure labels because a Rancher-managed RKE2 node is
// still operationally an RKE2 cluster.
func DetectNodePlatform(node corev1.Node) string {
	if platform := DetectPlatformFromVersion(node.Status.NodeInfo.KubeletVersion); platform != "unknown" {
		return platform
	}
	if platform := DetectByProviderID(node); platform != "unknown" {
		return platform
	}
	if platform := DetectByLabels(node); platform != "unknown" {
		return platform
	}
	return DetectByNodeName(node)
}

// DetectPlatformFromVersion classifies distribution markers embedded in Kubernetes versions.
func DetectPlatformFromVersion(gitVersion string) string {
	if strings.Contains(gitVersion, "+rke2r") {
		return "rke2"
	}
	return "unknown"
}

// DetectByProviderID detects the platform from a node's ProviderID field.
func DetectByProviderID(node corev1.Node) string {
	providerID := node.Spec.ProviderID
	switch {
	case strings.HasPrefix(providerID, "gce://") || strings.HasPrefix(providerID, "gke://"):
		return "gke"
	case strings.HasPrefix(providerID, "aws://"):
		return "eks"
	case strings.HasPrefix(providerID, "azure://"):
		return "aks"
	case strings.HasPrefix(providerID, "kind://"):
		return "kind"
	default:
		return "unknown"
	}
}

// DetectByLabels detects the platform from a node's labels.
func DetectByLabels(node corev1.Node) string {
	if IsNodeGKE(node) {
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

// DetectByNodeName detects the platform from a node's name (local dev clusters).
func DetectByNodeName(node corev1.Node) string {
	name := node.Name
	switch {
	case strings.Contains(name, "kind-"):
		return "kind"
	case name == "minikube" || strings.HasPrefix(name, "minikube-"):
		return "minikube"
	case name == "docker-desktop":
		return "docker-desktop"
	default:
		return "unknown"
	}
}

// IsNodeGKE returns true if the node belongs to a GKE cluster.
func IsNodeGKE(node corev1.Node) bool {
	if _, exists := node.Labels["cloud.google.com/gke-nodepool"]; exists {
		return true
	}
	if _, exists := node.Labels["cloud.google.com/gke-os-distribution"]; exists {
		return true
	}
	return strings.HasPrefix(node.Spec.ProviderID, "gce://")
}

// detectAutopilot checks whether a GKE cluster is running in Autopilot mode.
func detectAutopilot(ctx context.Context, client kubernetes.Interface) (bool, error) {
	nodeList, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err == nil && len(nodeList.Items) > 0 {
		node := nodeList.Items[0]
		if val, exists := node.Labels["cloud.google.com/gke-autopilot"]; exists && val == "true" {
			return true, nil
		}
		if !IsNodeGKE(node) {
			return false, nil
		}
	}

	// Check kube-system pod annotations for Autopilot marker
	podList, err := client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{Limit: 10})
	if err != nil {
		return false, err
	}
	for _, pod := range podList.Items {
		for key := range pod.Annotations {
			if strings.HasPrefix(key, "autopilot.gke.io/") {
				return true, nil
			}
		}
	}

	return false, nil
}
