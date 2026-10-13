package topology

import (
	"fmt"

	"github.com/skyhook-io/radar/pkg/netpol"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (b *Builder) addCiliumPolicyNodes(nodes []Node, edges []Edge, opts BuildOptions, warnings *[]string,
	deployments []*appsv1.Deployment, statefulsets []*appsv1.StatefulSet, daemonsets []*appsv1.DaemonSet,
) ([]Node, []Edge) {
	if b.dynamic == nil {
		return nodes, edges
	}
	type workload struct {
		id  string
		pod *corev1.Pod
	}
	var workloads []workload
	workloadsByNamespace := map[string][]workload{}
	existing := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		existing[node.ID] = true
	}
	add := func(kind, ns, name string, template corev1.PodTemplateSpec) {
		id := kind + "/" + ns + "/" + name
		if !existing[id] {
			return
		}
		entry := workload{id, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Labels: template.Labels}, Spec: template.Spec,
		}}
		workloads = append(workloads, entry)
		workloadsByNamespace[ns] = append(workloadsByNamespace[ns], entry)
	}
	for _, d := range deployments {
		add("deployment", d.Namespace, d.Name, d.Spec.Template)
	}
	for _, s := range statefulsets {
		add("statefulset", s.Namespace, s.Name, s.Spec.Template)
	}
	for _, d := range daemonsets {
		add("daemonset", d.Namespace, d.Name, d.Spec.Template)
	}
	var namespaces map[string]*corev1.Namespace
	nsLoaded := false
	identities := map[string]netpol.CiliumIdentity{}
	loadNamespaces := func() {
		if nsLoaded {
			return
		}
		nsLoaded = true
		namespaces = map[string]*corev1.Namespace{}
		if provider, ok := b.provider.(NamespaceProvider); ok {
			if items, err := provider.Namespaces(); err == nil {
				for _, ns := range items {
					namespaces[ns.Name] = ns
				}
			}
		}
		for _, workload := range workloads {
			identities[workload.id] = netpol.CiliumIdentityFor(workload.pod, namespaces[workload.pod.Namespace])
		}
	}
	unknownWorkloads := map[string][]string{}
	for _, definition := range []struct {
		kind       string
		nodeKind   NodeKind
		prefix     string
		namespaced bool
	}{
		{"CiliumNetworkPolicy", KindCiliumNetworkPolicy, "ciliumnetworkpolicy", true},
		{"CiliumClusterwideNetworkPolicy", KindCiliumClusterwideNetworkPolicy, "ciliumclusterwidenetworkpolicy", false},
	} {
		gvr, ok := b.dynamic.GetGVRWithGroup(definition.kind, "cilium.io")
		if !ok {
			continue
		}
		var items []*unstructured.Unstructured
		var err error
		if definition.namespaced {
			items, err = b.dynamic.ListNamespaces(gvr, opts.Namespaces)
		} else {
			items, err = b.dynamic.List(gvr, "")
		}
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("Failed to list %s: %v", definition.kind, err))
		}
		if len(items) == 0 {
			continue
		}
		loadNamespaces()
		for _, policy := range items {
			ns := policy.GetNamespace()
			if definition.namespaced && !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			id := definition.prefix + "/" + ns + "/" + policy.GetName()
			if !definition.namespaced {
				id = definition.prefix + "/" + policy.GetName()
			}
			data := map[string]any{"namespace": ns, "labels": policy.GetLabels(), "apiVersion": policy.GetAPIVersion()}
			nodes = append(nodes, Node{ID: id, Kind: definition.nodeKind, Name: policy.GetName(), Status: StatusHealthy, Data: data})
			matcher := netpol.CompileCiliumPolicy(policy)
			if matcher.SelectsAll() {
				data["matchesAllPods"] = true
				continue
			}
			candidates := workloads
			if definition.namespaced {
				candidates = workloadsByNamespace[ns]
			}
			for _, workload := range candidates {
				matches, decided, _ := matcher.Selects(identities[workload.id], "")
				if !decided {
					unknownWorkloads[workload.id] = append(unknownWorkloads[workload.id], id)
					continue
				}
				if matches {
					edges = append(edges, Edge{ID: id + "-to-" + workload.id, Source: id, Target: workload.id, Type: EdgeProtects})
				}
			}

		}
	}
	for i := range nodes {
		if sources := unknownWorkloads[nodes[i].ID]; len(sources) > 0 {
			nodes[i].networkPolicyMatchesIncompleteSources = sources
			nodes[i].Data["networkPolicyMatchesIncomplete"] = true
		}
	}

	return nodes, edges
}
