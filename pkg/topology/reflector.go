package topology

import (
	"fmt"
	"github.com/skyhook-io/radar/pkg/configrefs"
	corev1 "k8s.io/api/core/v1"
	"sort"
	"strings"
)

func (b *Builder) addReflectionRelationships(t *Topology, opts BuildOptions) {
	var objects []configrefs.Object
	observed := map[configrefs.Ref]Node{}
	if opts.IncludeConfigMaps {
		items, err := b.provider.ConfigMaps()
		if err == nil {
			for _, o := range items {
				if opts.MatchesNamespaceFilter(o.Namespace) {
					metadata := configrefs.Metadata("ConfigMap", o)
					objects = append(objects, metadata)
					observed[metadata.Ref] = configMapNode(o)
				}
			}
		}
	}
	if opts.IncludeSecrets {
		items, err := b.provider.Secrets()
		if err == nil {
			for _, o := range items {
				if opts.MatchesNamespaceFilter(o.Namespace) {
					metadata := configrefs.Metadata("Secret", o)
					objects = append(objects, metadata)
					observed[metadata.Ref] = secretNode(o)
				}
			}
		}
	}
	refs := configrefs.BuildReflections(objects)
	nodes := map[string]bool{}
	edges := map[string]bool{}
	for _, n := range t.Nodes {
		nodes[n.ID] = true
	}
	for _, e := range t.Edges {
		edges[e.ID] = true
	}
	id := func(ref configrefs.Ref) string {
		return strings.ToLower(ref.Kind) + "/" + ref.Namespace + "/" + ref.Name
	}
	for _, link := range refs.Links {
		for _, ref := range []configrefs.Ref{link.Source, link.Mirror} {
			key := id(ref)
			if !nodes[key] {
				t.Nodes = append(t.Nodes, observed[ref])
				nodes[key] = true
			}
		}
		source, target := id(link.Source), id(link.Mirror)
		key := fmt.Sprintf("%s-reflects-to-%s", source, target)
		if !edges[key] {
			t.Edges = append(t.Edges, Edge{ID: key, Source: source, Target: target, Type: EdgeConfigures, Label: "Reflects to"})
			edges[key] = true
		}
	}
}

func (t *Topology) SecretRBACTuples() []SARTuple {
	seen := map[string]bool{}
	var tuples []SARTuple
	if t == nil {
		return nil
	}
	for _, n := range t.Nodes {
		if n.Kind == KindSecret {
			ns := nodeNamespaceFromData(&n)
			if !seen[ns] {
				seen[ns] = true
				tuples = append(tuples, SARTuple{Resource: "secrets", Namespace: ns})
			}
		}
	}
	sort.Slice(tuples, func(i, j int) bool { return tuples[i].Namespace < tuples[j].Namespace })
	return tuples
}

func (t *Topology) StripSecretsExcept(allowed map[SARTuple]bool) {
	if t == nil {
		return
	}
	deny := map[string]bool{}
	for _, n := range t.Nodes {
		if n.Kind == KindSecret && !allowed[SARTuple{Resource: "secrets", Namespace: nodeNamespaceFromData(&n)}] {
			deny[n.ID] = true
		}
	}
	t.StripNodeIDs(deny)
}

func configMapNode(cm *corev1.ConfigMap) Node {
	return Node{ID: fmt.Sprintf("configmap/%s/%s", cm.Namespace, cm.Name), Kind: KindConfigMap, Name: cm.Name, Status: StatusHealthy, Data: map[string]any{"namespace": cm.Namespace, "keys": len(cm.Data), "labels": cm.Labels}}
}

func secretNode(secret *corev1.Secret) Node {
	return Node{ID: fmt.Sprintf("secret/%s/%s", secret.Namespace, secret.Name), Kind: KindSecret, Name: secret.Name, Status: StatusHealthy, Data: map[string]any{"namespace": secret.Namespace, "keys": len(secret.Data), "labels": secret.Labels, "type": string(secret.Type)}}
}
