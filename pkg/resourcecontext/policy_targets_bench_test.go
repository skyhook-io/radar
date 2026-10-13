package resourcecontext

import (
	"context"
	"fmt"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func BenchmarkBroadPolicyContext(b *testing.B) {
	for _, count := range []int{1500, 15000, 50000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			policy := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "cilium.io/v2", "kind": "CiliumClusterwideNetworkPolicy",
				"metadata": map[string]any{"name": "all"},
			}}
			graph := &topology.Topology{Nodes: []topology.Node{{
				ID: "ciliumclusterwidenetworkpolicy/all", Kind: topology.KindCiliumClusterwideNetworkPolicy, Name: "all",
				Data: map[string]any{"apiVersion": "cilium.io/v2", "matchesAllPods": true},
			}}}
			for i := 0; i < count; i++ {
				namespace, name := fmt.Sprintf("ns-%04d", i/30), fmt.Sprintf("web-%05d", i)
				graph.Nodes = append(graph.Nodes, topology.Node{
					ID: "deployment/" + namespace + "/" + name, Kind: topology.KindDeployment, Name: name,
					Data: map[string]any{"namespace": namespace, "apiVersion": "apps/v1"},
				})
			}
			index := topology.IndexByResource(graph)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rel := topology.GetRelationshipsWithObject("CiliumClusterwideNetworkPolicy", "", "all", policy, graph, nil, nil, index)
				rc := Build(context.Background(), policy, Options{Relationships: rel, AccessChecker: allowAllChecker{}})
				if len(rel.Protects) != count || len(rc.Protects) != maxPolicyTargetRefs {
					b.Fatalf("relationship targets=%d context targets=%d", len(rel.Protects), len(rc.Protects))
				}
			}
		})
	}
}
