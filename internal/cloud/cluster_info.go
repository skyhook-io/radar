package cloud

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// DiscoverAPIServerURL reads `kube-public/cluster-info` and returns the
// external API server URL the cluster advertises to clients. The hub
// stores this so the fleet GitOps view can correlate Argo CD's
// `destination.server` references against the hub's connected clusters.
//
// Returns "" (not an error) when:
//   - the ConfigMap doesn't exist (typical on managed K8s services —
//     EKS, GKE, AKS don't run the kubeadm bootstrap step that creates it)
//   - RBAC denies the read (cluster-info is system:unauthenticated-readable
//     by convention but some hardened deployments lock it down)
//   - the embedded kubeconfig has no clusters or no server URL
//
// All of the empty-string cases are fine: the hub falls back to name-based
// correlation. A non-empty URL just gives correlation a stronger signal.
//
// Practical reach: since EKS/GKE/AKS omit cluster-info, URL correlation
// is dormant on the cloud-K8s majority and only fires for kubeadm-style
// installs (kind, k3d, on-prem, kubeadm). Closing this gap is a chart-
// level escape hatch (helm value -> RADAR_API_SERVER_URL env var fed in
// at install time from the operator's kubeconfig), not an in-cluster
// discovery problem — pods can't see the external API server URL
// without external input. Tracked as a follow-up; not blocking.
//
// Caller should pass a short timeout via ctx — a single ConfigMap GET
// should resolve in well under a second.
func DiscoverAPIServerURL(ctx context.Context, client kubernetes.Interface) string {
	if client == nil {
		return ""
	}
	cm, err := client.CoreV1().ConfigMaps("kube-public").Get(ctx, "cluster-info", metav1.GetOptions{})
	if err != nil {
		// 404, RBAC, or transient error — all silent. The agent connect
		// path is best-effort for this field.
		return ""
	}
	kubeconfig, ok := cm.Data["kubeconfig"]
	if !ok || kubeconfig == "" {
		return ""
	}
	// clientcmd.Load parses the embedded kubeconfig YAML; we want the
	// canonical external API server URL that kubeadm writes for THIS
	// cluster (the one the agent is running in).
	cfg, err := clientcmd.Load([]byte(kubeconfig))
	if err != nil {
		return ""
	}
	// Prefer the cluster CurrentContext points at — for kubeadm-generated
	// cluster-info, CurrentContext is set and resolves unambiguously. If
	// that path doesn't yield a valid URL, fall back to picking from the
	// remaining clusters in sorted-key order (rare: federated kubeadm
	// configs with multiple cluster entries). Iterating cfg.Clusters
	// directly was non-deterministic — different invocations could return
	// different URLs on a multi-cluster kubeconfig, flaking hub correlation.
	if cfg.CurrentContext != "" {
		if kubeCtx := cfg.Contexts[cfg.CurrentContext]; kubeCtx != nil && kubeCtx.Cluster != "" {
			if cluster := cfg.Clusters[kubeCtx.Cluster]; cluster != nil {
				if u := validClusterServer(cluster.Server); u != "" {
					return u
				}
			}
		}
	}
	keys := make([]string, 0, len(cfg.Clusters))
	for k := range cfg.Clusters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cluster := cfg.Clusters[k]
		if cluster == nil {
			continue
		}
		if u := validClusterServer(cluster.Server); u != "" {
			return u
		}
	}
	return ""
}

// validClusterServer returns the trimmed server URL when it has an
// http(s) scheme, else empty. Keeps the wire clean — the hub validates
// again, but pruning garbage here avoids sending obvious junk.
func validClusterServer(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return ""
	}
	return s
}

// validateAPIServerURL is a defensive header-value check. We send what
// DiscoverAPIServerURL returns, but a corrupted ConfigMap shouldn't be
// able to inject newlines / null bytes / oversized garbage into the
// X-Radar-API-Server-URL header. Returns the input on success, "" on
// rejection. Caller logs at info level when it rejects so operators have
// a breadcrumb without spamming.
func validateAPIServerURL(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if len(s) > 512 {
		return "", fmt.Errorf("api server url too long (%d > 512)", len(s))
	}
	if strings.ContainsAny(s, "\n\r\x00") {
		return "", fmt.Errorf("api server url contains control characters")
	}
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return "", fmt.Errorf("api server url missing scheme")
	}
	return s, nil
}
