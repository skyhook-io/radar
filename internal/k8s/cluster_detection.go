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
	nkpAPIMu         sync.Mutex
	nkpAPIChecked    bool
	nkpAPIAnswer     bool
	nkpAPIGeneration uint64
)

var (
	serverVersionMu       sync.Mutex
	cachedServerVersion   string
	serverVersionInFlight *serverVersionRead
	serverVersionFailedAt time.Time
)

// serverVersionRetryAfter answers "" without a new read for a while after a
// failure, so callers during an outage don't each wait out the client timeout.
const serverVersionRetryAfter = 5 * time.Second

type serverVersionRead struct {
	done        chan struct{}
	version     string
	invalidated bool // guarded by serverVersionMu
}

// GetServerVersion returns the Kubernetes server version, read once per
// context. A failed read is not kept: a call after serverVersionRetryAfter asks
// again, while callers that arrive during a read wait for it instead of
// starting their own.
func GetServerVersion() string {
	serverVersionMu.Lock()
	if cachedServerVersion != "" {
		defer serverVersionMu.Unlock()
		return cachedServerVersion
	}
	if time.Since(serverVersionFailedAt) < serverVersionRetryAfter {
		serverVersionMu.Unlock()
		return ""
	}
	if read := serverVersionInFlight; read != nil {
		serverVersionMu.Unlock()
		<-read.done
		return serverVersionOrRetry(read)
	}
	read := &serverVersionRead{done: make(chan struct{})}
	serverVersionInFlight = read
	serverVersionMu.Unlock()

	if client := GetClient(); client != nil {
		if v, err := client.Discovery().ServerVersion(); err == nil {
			read.version = v.GitVersion
		}
	}

	serverVersionMu.Lock()
	// A context switch during the read invalidated it; don't cache the
	// previous cluster's version.
	if serverVersionInFlight == read {
		serverVersionInFlight = nil
		cachedServerVersion = read.version
		if read.version == "" {
			serverVersionFailedAt = time.Now()
		}
	}
	serverVersionMu.Unlock()
	close(read.done)
	return serverVersionOrRetry(read)
}

// serverVersionOrRetry asks again when a context switch invalidated the read,
// so no caller gets the previous cluster's version.
func serverVersionOrRetry(read *serverVersionRead) string {
	serverVersionMu.Lock()
	invalidated := read.invalidated
	serverVersionMu.Unlock()
	if invalidated {
		return GetServerVersion()
	}
	return read.version
}

// InvalidateServerVersionCache resets the cached server version.
// Called during context switch so the next call re-fetches.
func InvalidateServerVersionCache() {
	serverVersionMu.Lock()
	defer serverVersionMu.Unlock()
	cachedServerVersion = ""
	if serverVersionInFlight != nil {
		serverVersionInFlight.invalidated = true
	}
	serverVersionInFlight = nil
	serverVersionFailedAt = time.Time{}
}

// ClusterInfo contains detected cluster information
type ClusterInfo struct {
	Context            string `json:"context"`  // kubeconfig context name
	Cluster            string `json:"cluster"`  // cluster name from kubeconfig
	Platform           string `json:"platform"` // nkp, rke2, gke, gke-autopilot, eks, aks, nutanix, minikube, kind, docker-desktop, openshift, rancher, generic
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

type clusterInfoObservation struct {
	info ClusterInfo
	at   time.Time
}

// LastClusterInfo returns the last completed read with a known version for this cache generation.
// Diagnostics must not start or wait for an API request when the cluster is unreachable.
func (cache *ResourceCache) LastClusterInfo() (ClusterInfo, time.Time) {
	if observed := cache.lastClusterInfo.Load(); observed != nil {
		return observed.info, observed.at
	}
	return ClusterInfo{}, time.Time{}
}

// GetClusterInfo returns detected cluster information
func GetClusterInfo(ctx context.Context) (*ClusterInfo, error) {
	conn := Current()
	initialCache := conn.ResourceCache()
	platform, _ := GetClusterPlatform(ctx)

	info := &ClusterInfo{
		Context:   GetContextName(),
		Cluster:   GetClusterName(),
		Platform:  platform,
		InCluster: IsInCluster(),
	}

	// Get version info (cached — only fetched once per context)
	info.KubernetesVersion = GetServerVersion()

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
	} else if cache.CustomResourcesPending() {
		info.CRDDiscoveryStatus = string(k8score.CRDDiscoveryInProgress)
	}

	// The version is cached and the counts come from informers, so a read
	// during an outage succeeds on stale data: it must not move the
	// observation (and its time) forward.
	if initialCache != nil && conn.cacheIsCurrent() && ctx.Err() == nil && info.KubernetesVersion != "" &&
		GetConnectionStatus().State != StateDisconnected {
		initialCache.lastClusterInfo.Store(&clusterInfoObservation{info: *info, at: time.Now()})
	}
	return info, nil
}

// GetClusterPlatform attempts to detect the Kubernetes platform/provider
func GetClusterPlatform(ctx context.Context) (string, error) {
	if IsNKP(ctx) {
		return "nkp", nil
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

// IsNKP reports whether the cluster has the NKP namespace, as Radar's own
// ServiceAccount sees it, whatever the signed-in user may read. A denied read
// counts as no. It trusts the namespace cache only once it holds namespaces:
// an empty list may just be unsynced. Otherwise it asks the API once per
// cluster, with a short limit so cluster info never waits on a slow
// namespace read.
func IsNKP(ctx context.Context) bool {
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
	nkpAPIMu.Lock()
	if nkpAPIChecked {
		answer := nkpAPIAnswer
		nkpAPIMu.Unlock()
		return answer
	}
	generation := nkpAPIGeneration
	nkpAPIMu.Unlock()
	client := GetClient()
	if client == nil {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, nkpLookupTimeout)
	defer cancel()
	_, err := client.CoreV1().Namespaces().Get(lookupCtx, k8score.NKPNamespace, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return false
	}
	nkpAPIMu.Lock()
	defer nkpAPIMu.Unlock()
	if nkpAPIGeneration != generation {
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
	nkpAPIGeneration++
	nkpAPIChecked, nkpAPIAnswer = false, false
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
		if !k8score.IsNodeGKE(node) {
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
	isAutopilot, found := checkAutopilotViaAnnotations(ctx)
	if found && isAutopilot {
		return "gke-autopilot", nil
	}
	if platform := k8score.DetectPlatformFromVersion(GetServerVersion()); platform != "unknown" {
		return platform, nil
	}
	return "unknown", nil
}
