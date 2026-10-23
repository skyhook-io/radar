package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
)

type runtimeVersionsInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"limit to one namespace; omit for every namespace you can list Pods in"`
	Runtime   string `json:"runtime,omitempty" jsonschema:"keep images declaring this runtime (php, node, python, go, ruby, java, nginx, postgres, …) plus unreadable ones"`
	FixedIn   string `json:"fixed_in,omitempty" jsonschema:"first fixed release per branch, comma-separated, e.g. 8.2.35,8.3.36"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max images (default 50, max 200); below-fix first"`
}

// runtimeVersionsResponse adds what a reader needs to weigh the inventory.
type runtimeVersionsResponse struct {
	images.Inventory
	Basis string `json:"basis"`
	// NamespacesWithoutPodAccess lists requested namespaces whose Pods the
	// caller may not list, so their absence isn't read as "nothing runs".
	NamespacesWithoutPodAccess []string `json:"namespacesWithoutPodAccess,omitempty"`
}

const runtimeVersionsBasis = "Versions an image declares in its config (Docker official images and images built FROM them), read for the digest each Pod runs. Declared, not proof of the binary; runtimes installed from distro packages declare nothing here. resolvedFrom spec or tag means the kubelet reported no registry digest. These images and assumed platforms get no fix verdict. Missing declarations remain unknown, not absent. Unsupported or distro version formats are unparsed. Node registry credentials are not used; private images pulled only with node identity may remain unreadable."

func handleGetRuntimeVersions(ctx context.Context, _ *mcp.CallToolRequest, input runtimeVersionsInput) (*mcp.CallToolResult, any, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, errNotConnected()
	}
	if cache.Pods() == nil {
		return nil, nil, fmt.Errorf("pods are not readable in this cluster, so running images are unknown")
	}
	var fixedIn []string
	for _, v := range strings.Split(input.FixedIn, ",") {
		if v = strings.TrimSpace(v); v != "" {
			fixedIn = append(fixedIn, v)
		}
	}
	if len(fixedIn) > 0 && strings.TrimSpace(input.Runtime) == "" {
		return nil, nil, fmt.Errorf("fixed_in needs runtime: fixed releases belong to one runtime (e.g. runtime=php)")
	}
	input.Runtime = strings.ToLower(strings.TrimSpace(input.Runtime))
	if err := images.ValidateRuntimeQuery(input.Runtime, fixedIn); err != nil {
		return nil, nil, err
	}
	if input.Namespace != "" && !namespaceWithinPin(input.Namespace) {
		return nil, nil, fmt.Errorf("%s: namespace %q is outside Radar's configured scope", deniedScopeReason([]string{input.Namespace}), input.Namespace)
	}
	pods, denied := runtimePods(ctx, cache, input.Namespace)
	user, _ := resolveUserPerms(ctx)

	inv := images.DefaultRuntimeInventory(k8s.IsInCluster()).Inventory(ctx, images.PodSource{
		Pods: pods,
		Workload: func(pod *corev1.Pod) (string, string) {
			if w, ok := k8s.PodWorkload(cache, pod); ok {
				return w.Kind, w.Name
			}
			return "Pod", pod.Name
		},
		Platform: func(node string) (v1.Platform, bool) {
			if cache.Nodes() == nil {
				return v1.Platform{}, false
			}
			n, err := cache.Nodes().Get(node)
			if err != nil || n.Status.NodeInfo.Architecture == "" {
				return v1.Platform{}, false
			}
			return v1.Platform{OS: n.Status.NodeInfo.OperatingSystem, Architecture: n.Status.NodeInfo.Architecture}, true
		},
		PullSecrets: func(pod *corev1.Pod) []string {
			secrets := make([]string, 0, len(pod.Spec.ImagePullSecrets))
			for _, ref := range pod.Spec.ImagePullSecrets {
				if ref.Name != "" {
					secrets = append(secrets, ref.Name)
				}
			}
			return secrets
		},
	}, images.InventoryOptions{
		Runtime: input.Runtime,
		FixedIn: fixedIn,
		Limit:   input.Limit,
		// Radar's own registry credentials are the user's own only when
		// Radar runs on their machine with auth off; in-cluster they belong
		// to Radar's ServiceAccount, even without auth.
		RadarCredentials: user == nil && !k8s.IsInCluster(),
	})

	return toJSONResult(runtimeVersionsResponse{Inventory: inv, Basis: runtimeVersionsBasis, NamespacesWithoutPodAccess: denied})
}

// runtimePods returns the Pods the caller may list, and which asked-for
// namespaces they may not. Namespace visibility alone isn't enough: it admits
// a namespace where the caller can list Deployments but not Pods, and
// reading those Pods' images (with their pull secrets) would go past what
// the caller can see.
func runtimePods(ctx context.Context, cache *k8s.ResourceCache, namespace string) (pods []*corev1.Pod, denied []string) {
	var requested []string
	if namespace != "" {
		requested = []string{namespace}
	}
	scope := scopedNamespacesForUser(ctx, requested)
	if scope != nil && len(scope) == 0 {
		return nil, requested
	}

	if scope == nil {
		all, _ := cache.Pods().List(labels.Everything())
		if canReadInNamespace(ctx, "", "pods", "", "list") {
			return all, nil
		}
		scope = podNamespaces(all)
	}
	allowed := filterNamespacesByCanRead(ctx, "", "pods", "list", scope)
	for _, ns := range scope {
		if !slices.Contains(allowed, ns) && slices.Contains(requested, ns) {
			denied = append(denied, ns)
		}
	}
	for _, ns := range allowed {
		list, _ := cache.Pods().Pods(ns).List(labels.Everything())
		pods = append(pods, list...)
	}
	return pods, denied
}

func podNamespaces(pods []*corev1.Pod) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range pods {
		if !seen[p.Namespace] {
			seen[p.Namespace] = true
			out = append(out, p.Namespace)
		}
	}
	slices.Sort(out)
	return out
}
