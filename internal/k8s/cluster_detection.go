package k8s

import (
	"context"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/pkg/k8score"
)

var (
	cachedServerVersion string
	serverVersionOnce   = new(sync.Once)
	serverVersionMu     sync.Mutex
)

// getServerVersion returns the cached Kubernetes server version.
// The version is fetched once and cached for the lifetime of the context.
func getServerVersion() string {
	serverVersionMu.Lock()
	defer serverVersionMu.Unlock()
	serverVersionOnce.Do(func() {
		if k8sClient != nil {
			if v, err := k8sClient.Discovery().ServerVersion(); err == nil {
				cachedServerVersion = v.GitVersion
			}
		}
	})
	return cachedServerVersion
}

// InvalidateServerVersionCache resets the cached server version.
// Called during context switch so the next call re-fetches.
func InvalidateServerVersionCache() {
	serverVersionMu.Lock()
	defer serverVersionMu.Unlock()
	cachedServerVersion = ""
	serverVersionOnce = new(sync.Once)
}

// ClusterInfo contains detected cluster information
type ClusterInfo struct {
	Context            string `json:"context"`  // kubeconfig context name
	Cluster            string `json:"cluster"`  // cluster name from kubeconfig
	Platform           string `json:"platform"` // gke, gke-autopilot, eks, aks, minikube, kind, docker-desktop, generic
	KubernetesVersion  string `json:"kubernetesVersion"`
	NodeCount          int    `json:"nodeCount"`
	PodCount           int    `json:"podCount"`
	NamespaceCount     int    `json:"namespaceCount"`
	InCluster          bool   `json:"inCluster"`                    // true when running inside a K8s cluster
	CRDDiscoveryStatus string `json:"crdDiscoveryStatus,omitempty"` // idle, discovering, ready
}

// GetClusterInfo returns detected cluster information
func GetClusterInfo(ctx context.Context) (*ClusterInfo, error) {
	platform, _ := GetClusterPlatform(ctx)

	info := &ClusterInfo{
		Context:   GetContextName(),
		Cluster:   GetClusterName(),
		Platform:  platform,
		InCluster: IsInCluster(),
	}

	// Get version info (cached — only fetched once per context)
	info.KubernetesVersion = getServerVersion()

	// Get counts from cache (listers may be nil when RBAC restricts access)
	cache := GetResourceCache()
	if cache != nil {
		if nodeLister := cache.Nodes(); nodeLister != nil {
			if nodes, err := nodeLister.List(labels.Everything()); err == nil {
				info.NodeCount = len(nodes)
			}
		}
		if podLister := cache.Pods(); podLister != nil {
			if pods, err := podLister.List(labels.Everything()); err == nil {
				info.PodCount = len(pods)
			}
		}
		if nsLister := cache.Namespaces(); nsLister != nil {
			if namespaces, err := nsLister.List(labels.Everything()); err == nil {
				info.NamespaceCount = len(namespaces)
			}
		}
	}

	// Get CRD discovery status
	dynamicCache := GetDynamicResourceCache()
	if dynamicCache != nil {
		info.CRDDiscoveryStatus = string(dynamicCache.GetDiscoveryStatus())
	}

	return info, nil
}

// GetClusterPlatform attempts to detect the Kubernetes platform/provider
func GetClusterPlatform(ctx context.Context) (string, error) {
	var nodes []corev1.Node
	cache := GetResourceCache()
	if cache != nil {
		if nodeLister := cache.Nodes(); nodeLister != nil {
			nodeList, err := nodeLister.List(labels.Everything())
			if err == nil && len(nodeList) > 0 {
				for _, n := range nodeList {
					nodes = append(nodes, *n)
				}
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
		if nodeLister := cache.Nodes(); nodeLister != nil {
			nodeList, err := nodeLister.List(labels.Everything())
			if err == nil && len(nodeList) > 0 {
				for _, n := range nodeList {
					nodes = append(nodes, *n)
				}
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
		if podLister := cache.Pods(); podLister != nil {
			podList, err := podLister.Pods("kube-system").List(labels.Everything())
			if err == nil && len(podList) > 0 {
				for i, pod := range podList {
					pods = append(pods, *pod)
					if i >= 9 {
						break
					}
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

// Pure helpers delegate to pkg/k8score for reuse without singletons.
func detectByProviderID(node corev1.Node) string { return k8score.DetectByProviderID(node) }
func detectByLabels(node corev1.Node) string     { return k8score.DetectByLabels(node) }
func detectByNodeName(node corev1.Node) string   { return k8score.DetectByNodeName(node) }
func isNodeGKE(node corev1.Node) bool            { return k8score.IsNodeGKE(node) }

func detectPlatformFallback(ctx context.Context) (string, error) {
	isAutopilot, found := checkAutopilotViaAnnotations(ctx)
	if found && isAutopilot {
		return "gke-autopilot", nil
	}
	return "unknown", nil
}
