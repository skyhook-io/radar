package topology

import "strings"

// ManagedResourceSet returns GitOps-managed topology resources and their
// descendants. It deliberately ignores native Helm metadata: Radar's
// managed-only view is for Argo CD and Flux ownership, not every Helm install.
func ManagedResourceSet(t *Topology) map[string]bool {
	set := map[string]bool{}
	if t == nil {
		return set
	}
	seed := map[string]bool{}
	for _, n := range t.Nodes {
		if isGitOpsNode(n) {
			seed[n.ID] = true
			set[managedKey(string(n.Kind), nodeNamespace(n), n.Name)] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for _, e := range t.Edges {
			if e.Type != EdgeManages || !seed[e.Source] || seed[e.Target] {
				continue
			}
			seed[e.Target] = true
			changed = true
		}
	}
	for _, n := range t.Nodes {
		if seed[n.ID] {
			set[managedKey(string(n.Kind), nodeNamespace(n), n.Name)] = true
		}
	}
	return set
}

// StripUnmanaged removes resources outside the Argo/Flux ownership closure.
func (t *Topology) StripUnmanaged() {
	if t == nil {
		return
	}
	managed := ManagedResourceSet(t)
	dropped := map[string]bool{}
	kept := t.Nodes[:0]
	for _, n := range t.Nodes {
		if managed[managedKey(string(n.Kind), nodeNamespace(n), n.Name)] {
			kept = append(kept, n)
		} else {
			dropped[n.ID] = true
		}
	}
	t.Nodes = kept
	edges := t.Edges[:0]
	for _, e := range t.Edges {
		if !dropped[e.Source] && !dropped[e.Target] {
			edges = append(edges, e)
		}
	}
	t.Edges = edges
}

func managedKey(kind, namespace, name string) string {
	return strings.ToLower(kind) + "\x00" + namespace + "\x00" + name
}

func ManagedResourceKey(kind, namespace, name string) string {
	return managedKey(kind, namespace, name)
}

func nodeNamespace(n Node) string {
	if n.Data == nil {
		return ""
	}
	if ns, ok := n.Data["namespace"].(string); ok {
		return ns
	}
	return ""
}

func NodeNamespace(n Node) string { return nodeNamespace(n) }

func isGitOpsNode(n Node) bool {
	if n.Kind == KindApplication || n.Kind == KindKustomization || n.Kind == KindHelmRelease {
		return true
	}
	labels, _ := n.Data["labels"].(map[string]string)
	annotations, _ := n.Data["annotations"].(map[string]string)
	if labels == nil {
		if raw, ok := n.Data["labels"].(map[string]any); ok {
			labels = stringMap(raw)
		}
	}
	if annotations == nil {
		if raw, ok := n.Data["annotations"].(map[string]any); ok {
			annotations = stringMap(raw)
		}
	}
	if annotations["argocd.argoproj.io/tracking-id"] != "" {
		return true
	}
	if labels["argocd.argoproj.io/instance"] != "" {
		return true
	}
	return labels["kustomize.toolkit.fluxcd.io/name"] != "" && labels["kustomize.toolkit.fluxcd.io/namespace"] != "" ||
		labels["helm.toolkit.fluxcd.io/name"] != "" && labels["helm.toolkit.fluxcd.io/namespace"] != ""
}

func stringMap(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// IsManagedResource reports whether a resource belongs to a managed-only
// topology projection. Direct GitOps markers win; descendants are matched by
// the topology closure.
func IsManagedResource(t *Topology, kind, namespace, name string, labels, annotations map[string]string) bool {
	if annotations["argocd.argoproj.io/tracking-id"] != "" || labels["argocd.argoproj.io/instance"] != "" {
		return true
	}
	if (labels["kustomize.toolkit.fluxcd.io/name"] != "" && labels["kustomize.toolkit.fluxcd.io/namespace"] != "") ||
		(labels["helm.toolkit.fluxcd.io/name"] != "" && labels["helm.toolkit.fluxcd.io/namespace"] != "") {
		return true
	}
	return ManagedResourceSet(t)[managedKey(kind, namespace, name)]
}
