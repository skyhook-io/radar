package k8s

import (
	"context"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/pkg/k8score"
)

var nkpLookupTimeout = 2 * time.Second

var (
	nkpAPIMu      sync.Mutex
	nkpAPIChecked bool
	nkpAPIAnswer  bool
)

var (
	cachedServerVersion      string
	lastServerVersionFailure time.Time
	serverVersionMu          sync.Mutex
)

// GetServerVersion returns the cached Kubernetes server version.
// A successful, nonempty version is cached for the lifetime of the context.
// Failed reads wait 30 seconds before retrying so callers do not queue API requests.
func GetServerVersion() string {
	return getServerVersion(context.Background())
}

func getServerVersion(ctx context.Context) string {
	if ctx.Err() != nil {
		return ""
	}
	serverVersionMu.Lock()
	defer serverVersionMu.Unlock()
	if ctx.Err() != nil {
		return ""
	}
	if cachedServerVersion != "" {
		return cachedServerVersion
	}
	if time.Since(lastServerVersionFailure) < 30*time.Second {
		return ""
	}
	if client := GetClient(); client != nil {
		if v, err := client.Discovery().ServerVersionWithContext(ctx); err == nil && v != nil {
			cachedServerVersion = v.GitVersion
		}
		if cachedServerVersion == "" && ctx.Err() != context.Canceled {
			lastServerVersionFailure = time.Now()
		}
	}
	return cachedServerVersion
}

// InvalidateServerVersionCache resets the cached server version.
// Called during context switch so the next call re-fetches.
func InvalidateServerVersionCache() {
	serverVersionMu.Lock()
	defer serverVersionMu.Unlock()
	cachedServerVersion = ""
	lastServerVersionFailure = time.Time{}
}

// ClusterInfo contains detected cluster information
type ClusterInfo struct {
	Context            string `json:"context"`  // kubeconfig context name
	Cluster            string `json:"cluster"`  // cluster name from kubeconfig
	Platform           string `json:"platform"` // nkp, rke2, k3s, gke, gke-autopilot, eks, aks, nutanix, minikube, kind, docker-desktop, openshift, rancher, generic
	KubernetesVersion  string `json:"kubernetesVersion"`
	NodeCount          int    `json:"nodeCount"`
	PodCount           int    `json:"podCount"`
	NamespaceCount     int    `json:"namespaceCount"`
	InCluster          bool   `json:"inCluster"`                    // true when running inside a K8s cluster
	CRDDiscoveryStatus string `json:"crdDiscoveryStatus,omitempty"` // idle, discovering, ready
}

// DisplayName is the kubeconfig context, which names the cluster the way the
// user picked it: several contexts can share one cluster entry (GitLab agents,
// Teleport). In-cluster mode, and a context-less kubeconfig, fall back to Cluster.
func (i *ClusterInfo) DisplayName() string {
	if i.InCluster || i.Context == "" {
		return i.Cluster
	}
	return i.Context
}

// KubeconfigCluster is the kubeconfig cluster entry the context points at, or
// "" in in-cluster mode, where Cluster holds only the "in-cluster" sentinel.
func (i *ClusterInfo) KubeconfigCluster() string {
	if i.InCluster {
		return ""
	}
	return i.Cluster
}

// GetClusterInfo returns detected cluster information
func GetClusterInfo(ctx context.Context) (*ClusterInfo, error) {
	platform, err := GetClusterPlatform(ctx)
	if err != nil {
		return nil, err
	}

	info := &ClusterInfo{
		Context:   GetContextName(),
		Cluster:   GetClusterName(),
		Platform:  platform,
		InCluster: IsInCluster(),
	}

	// Get version info (cached — only fetched once per context)
	info.KubernetesVersion = getServerVersion(ctx)

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
	if err := ctx.Err(); err != nil {
		return "unknown", err
	}
	if isNKP(ctx) {
		return "nkp", nil
	}

	version := getServerVersion(ctx)
	if err := ctx.Err(); err != nil {
		return "unknown", err
	}
	if platform := detectPlatformFromServerVersion(version); platform != "unknown" {
		if platform == "gke" {
			if isAutopilot, _ := isGKEAutopilot(ctx, true); isAutopilot {
				return "gke-autopilot", nil
			}
		}
		return platform, nil
	}

	var node *corev1.Node
	cache := GetResourceCache()
	if cache != nil {
		if nodeLister := cache.Nodes(); nodeLister != nil {
			nodeList, err := nodeLister.List(labels.Everything())
			if err == nil && len(nodeList) > 0 {
				node = nodeList[0]
				for _, n := range nodeList {
					if k8score.DetectNodePlatform(*n) != "unknown" {
						node = n
						break
					}
				}
			}
		}
	}

	// Fallback to direct API if cache unavailable
	if client := GetClient(); node == nil && client != nil {
		nodeList, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil || len(nodeList.Items) == 0 {
			return detectPlatformFallback(ctx)
		}
		node = &nodeList.Items[0]
	}

	if node == nil {
		return detectPlatformFallback(ctx)
	}

	platform := k8score.DetectNodePlatform(*node)
	if platform != "unknown" {
		if platform == "gke" {
			if isAutopilot, _ := IsGKEAutopilot(ctx); isAutopilot {
				return "gke-autopilot", nil
			}
		}
		return platform, nil
	}

	return "generic", nil
}

func detectPlatformFromServerVersion(version string) string {
	switch {
	case strings.Contains(version, "+rke2r"):
		return "rke2"
	case strings.Contains(version, "-eks-"):
		return "eks"
	case strings.Contains(version, "-gke."):
		return "gke"
	case strings.Contains(version, "+k3s"):
		return "k3s"
	default:
		return "unknown"
	}
}

// isNKP trusts the namespace cache only once it holds namespaces: an empty
// list may just be unsynced. Otherwise it asks the API once per cluster, with
// a short limit so cluster info never waits on a slow namespace read.
func isNKP(ctx context.Context) bool {
	if cache := GetResourceCache(); cache != nil {
		if nsLister := cache.Namespaces(); nsLister != nil {
			if nsList, err := nsLister.List(labels.Everything()); err == nil && len(nsList) > 0 {
				for _, ns := range nsList {
					if ns.Name == k8score.NKPNamespace {
						return true
					}
				}
				return false
			}
		}
	}
	client := GetClient()
	if client == nil {
		return false
	}

	nkpAPIMu.Lock()
	defer nkpAPIMu.Unlock()
	if nkpAPIChecked {
		return nkpAPIAnswer
	}
	lookupCtx, cancel := context.WithTimeout(ctx, nkpLookupTimeout)
	defer cancel()
	_, err := client.CoreV1().Namespaces().Get(lookupCtx, k8score.NKPNamespace, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return false
	}
	nkpAPIChecked, nkpAPIAnswer = true, err == nil
	return nkpAPIAnswer
}

// InvalidatePlatformCache forgets the NKP answer read from the API.
// Called during context switch so the next call asks the new cluster.
func InvalidatePlatformCache() {
	nkpAPIMu.Lock()
	defer nkpAPIMu.Unlock()
	nkpAPIChecked, nkpAPIAnswer = false, false
}

// IsGKEAutopilot detects if the cluster is GKE Autopilot
func IsGKEAutopilot(ctx context.Context) (bool, error) {
	return isGKEAutopilot(ctx, false)
}

func isGKEAutopilot(ctx context.Context, controlPlaneGKE bool) (bool, error) {
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

	if client := GetClient(); len(nodes) == 0 && client != nil {
		nodeList, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
		if err == nil && len(nodeList.Items) > 0 {
			nodes = nodeList.Items
		}
	}

	if len(nodes) > 0 {
		node := nodes[0]
		if val, exists := node.Labels["cloud.google.com/gke-autopilot"]; exists && val == "true" {
			return true, nil
		}
		if !controlPlaneGKE && !k8score.IsNodeGKE(node) {
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

	if client := GetClient(); len(pods) == 0 && client != nil {
		podList, err := client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{Limit: 10})
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

func detectPlatformFallback(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "unknown", err
	}
	isAutopilot, found := checkAutopilotViaAnnotations(ctx)
	if found && isAutopilot {
		return "gke-autopilot", nil
	}
	if platform := k8score.DetectPlatformFromVersion(getServerVersion(ctx)); platform != "unknown" {
		return platform, nil
	}
	return "unknown", nil
}
