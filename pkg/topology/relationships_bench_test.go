package topology

import (
	"fmt"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"strings"
	"testing"
)

// buildSyntheticTopology returns a topology with ~targetNodes nodes shaped
// like a realistic workload tree: namespaces × (Deployments + each their
// ReplicaSet + N Pods + a Service exposing them + a ConfigMap and Secret
// referenced by each Deployment + a PDB and NetworkPolicy selecting each).
//
// Edge count grows roughly linearly with node count, giving a 5k-node
// topology around 6-8k edges — representative of a busy production cluster.
func buildSyntheticTopology(targetNodes int) *Topology {
	// Per-deployment: 1 Deployment + 1 ReplicaSet + 3 Pods + 1 Service + 1
	// ConfigMap + 1 Secret + 1 PDB + 1 NetworkPolicy = 10 nodes. Edges per
	// deployment: deploy->rs, rs->3pods, svc->3pods, cm->deploy, secret->deploy,
	// pdb->deploy, np->deploy = 11 edges.
	perDeploy := 10
	deploysPerNS := 10
	namespaces := targetNodes / (perDeploy * deploysPerNS)
	if namespaces < 1 {
		namespaces = 1
	}

	topo := &Topology{}
	for nsIdx := range namespaces {
		ns := fmt.Sprintf("ns-%d", nsIdx)
		for d := range deploysPerNS {
			deployName := fmt.Sprintf("app-%d", d)
			rsName := fmt.Sprintf("app-%d-rs", d)
			svcName := fmt.Sprintf("app-%d-svc", d)
			cmName := fmt.Sprintf("app-%d-config", d)
			secretName := fmt.Sprintf("app-%d-secret", d)
			pdbName := fmt.Sprintf("app-%d-pdb", d)
			npName := fmt.Sprintf("app-%d-np", d)

			deployID := fmt.Sprintf("deployment/%s/%s", ns, deployName)
			rsID := fmt.Sprintf("replicaset/%s/%s", ns, rsName)
			svcID := fmt.Sprintf("service/%s/%s", ns, svcName)
			cmID := fmt.Sprintf("configmap/%s/%s", ns, cmName)
			secretID := fmt.Sprintf("secret/%s/%s", ns, secretName)
			pdbID := fmt.Sprintf("poddisruptionbudget/%s/%s", ns, pdbName)
			npID := fmt.Sprintf("networkpolicy/%s/%s", ns, npName)

			topo.Nodes = append(topo.Nodes,
				Node{ID: deployID, Kind: KindDeployment, Name: deployName},
				Node{ID: rsID, Kind: KindReplicaSet, Name: rsName},
				Node{ID: svcID, Kind: KindService, Name: svcName},
				Node{ID: cmID, Kind: KindConfigMap, Name: cmName},
				Node{ID: secretID, Kind: KindSecret, Name: secretName},
				Node{ID: pdbID, Kind: KindPDB, Name: pdbName},
				Node{ID: npID, Kind: KindNetworkPolicy, Name: npName},
			)
			topo.Edges = append(topo.Edges,
				Edge{ID: deployID + "->rs", Source: deployID, Target: rsID, Type: EdgeManages},
				Edge{ID: cmID + "->deploy", Source: cmID, Target: deployID, Type: EdgeConfigures},
				Edge{ID: secretID + "->deploy", Source: secretID, Target: deployID, Type: EdgeConfigures},
				Edge{ID: pdbID + "->deploy", Source: pdbID, Target: deployID, Type: EdgeProtects},
				Edge{ID: npID + "->deploy", Source: npID, Target: deployID, Type: EdgeProtects},
			)

			for p := range 3 {
				podName := fmt.Sprintf("app-%d-pod-%d", d, p)
				podID := fmt.Sprintf("pod/%s/%s", ns, podName)
				topo.Nodes = append(topo.Nodes, Node{ID: podID, Kind: KindPod, Name: podName})
				topo.Edges = append(topo.Edges,
					Edge{ID: rsID + "->" + podName, Source: rsID, Target: podID, Type: EdgeManages},
					Edge{ID: svcID + "->" + podName, Source: svcID, Target: podID, Type: EdgeRoutesTo},
				)
			}
		}
	}
	return topo
}

// BenchmarkGetRelationships_NoIndex measures the cost of GetRelationships on a
// synthetic ~5k-node topology with no precomputed index. Each call rescans
// topo.Edges in full, so cost is O(E) per call — what every consumer pays
// today.
func BenchmarkGetRelationships_NoIndex(b *testing.B) {
	topo := buildSyntheticTopology(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nsIdx := i % 50
		_ = GetRelationships("Deployment", fmt.Sprintf("ns-%d", nsIdx), "app-0", topo, nil, nil)
	}
}

// BenchmarkGetRelationships_WithIndex measures the same call shape but with
// a precomputed RelationshipsIndex shared across calls. Edge lookups become
// O(degree(node)) instead of O(E). The index build cost is amortized: T6 /
// T12 build it once per topology refresh (every 5s) and reuse it for many
// per-resource queries.
func BenchmarkGetRelationships_WithIndex(b *testing.B) {
	topo := buildSyntheticTopology(5000)
	idx := IndexByResource(topo)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nsIdx := i % 50
		_ = GetRelationshipsWithIndex("Deployment", fmt.Sprintf("ns-%d", nsIdx), "app-0", topo, nil, nil, idx)
	}
}

// BenchmarkIndexByResource measures the one-time index build cost so callers
// can reason about whether caching the index pays for itself. With ~5k nodes
// and ~6k edges, the build is dominated by map allocation, not iteration.
func BenchmarkIndexByResource(b *testing.B) {
	topo := buildSyntheticTopology(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IndexByResource(topo)
	}
}

func BenchmarkGetRelationships_PodConsumersPerRequest(b *testing.B) {
	topo := buildSyntheticTopology(5000)
	topo.podConsumers = map[string][]ResourceRef{}
	for i := range topo.Nodes {
		node := &topo.Nodes[i]
		parts := strings.Split(node.ID, "/")
		ns := parts[1]
		node.Data = map[string]any{"namespace": ns}
		if node.Kind != KindPod {
			continue
		}
		ref := ResourceRef{Kind: "Pod", Namespace: ns, Name: node.Name}
		app := strings.Split(node.Name, "-pod-")[0]
		for _, target := range []ResourceRef{{Kind: "ConfigMap", Namespace: ns, Name: app + "-config"}, {Kind: "Secret", Namespace: ns, Name: app + "-secret"}, {Kind: "ConfigMap", Namespace: ns, Name: "kube-root-ca.crt"}, {Kind: "ServiceAccount", Namespace: ns, Name: "default"}} {
			key := resourceid.ResourceKey("", target.Kind, target.Namespace, target.Name)
			topo.podConsumers[key] = append(topo.podConsumers[key], ref)
		}
	}
	for i := range 50 {
		ns := fmt.Sprintf("ns-%d", i)
		topo.Nodes = append(topo.Nodes, Node{ID: "configmap/" + ns + "/kube-root-ca.crt", Kind: KindConfigMap, Name: "kube-root-ca.crt", Data: map[string]any{"namespace": ns}}, Node{ID: "serviceaccount/" + ns + "/default", Kind: KindServiceAccount, Name: "default", Data: map[string]any{"namespace": ns}})
	}
	for _, target := range []struct{ kind, name string }{{"Deployment", "app-0"}, {"ConfigMap", "app-0-config"}, {"ConfigMap", "kube-root-ca.crt"}} {
		b.Run(target.kind+"/"+target.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = GetRelationshipsWithObject(target.kind, "ns-0", target.name, nil, topo, nil, nil, nil)
			}
		})
	}
}
