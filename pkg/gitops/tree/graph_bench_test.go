package tree

import (
	"fmt"
	"testing"
)

func BenchmarkMaterialize(b *testing.B) {
	kinds := []string{"Deployment", "Pod", "ReplicaSet", "ConfigMap", "Widget"}
	nodes := make(map[string]Node, 1000)
	for i := range 1000 {
		name := fmt.Sprintf("resource-%04d", i)
		nodes[name] = Node{ID: name, Role: RoleGenerated, Ref: ResourceRef{Kind: kinds[i%len(kinds)], Namespace: "app", Name: name}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		materialize(nodes, nil)
	}
}
