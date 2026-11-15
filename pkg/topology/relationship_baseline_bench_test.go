package topology

import (
	"runtime"
	"slices"
	"testing"
)

// The benchmarks in this file record what the all-namespace relationship
// graph costs today, on the same fixtures the cache-index layer that replaces
// it is measured on. They change no behavior; docs/loadtest.md lists how to
// run them and the baselines they produced.
//
// Fixtures: L is the field census (fieldShape: 191,504 objects, 29,455 Pods);
// XL scales every kind by 10/3, about 98,000 Pods.
var relationshipFixtures = []struct {
	name     string
	num, den int
}{
	{"L", 1, 1},
	{"XL", 10, 3},
}

// serverRelationshipOptions are the options the SSE broadcaster builds the
// shared relationship graph with (buildFullTopology in internal/server).
func serverRelationshipOptions() BuildOptions {
	opts := RelationshipBuildOptions()
	opts.ViewMode = ViewModeResources
	opts.IncludeSecrets = true
	return opts
}

func heapInUse() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// BenchmarkRelationshipGraphRetained reports the heap the shared relationship
// graph and its lookup index keep while Radar holds them, which is for as long
// as it runs once the first relationship read or broadcast has built them.
func BenchmarkRelationshipGraphRetained(b *testing.B) {
	for _, f := range relationshipFixtures {
		b.Run(f.name, func(b *testing.B) {
			provider := newSyntheticProvider(fieldShape().fraction(f.num, f.den))
			opts := serverRelationshipOptions()
			var graphBytes, indexBytes float64
			var nodes, edges int
			for b.Loop() {
				before := heapInUse()
				topo, err := NewBuilder(provider).Build(opts)
				if err != nil {
					b.Fatalf("build: %v", err)
				}
				withGraph := heapInUse()
				index := IndexByResource(topo)
				withIndex := heapInUse()
				graphBytes += float64(withGraph) - float64(before)
				indexBytes += float64(withIndex) - float64(withGraph)
				nodes, edges = len(topo.Nodes), len(topo.Edges)
				runtime.KeepAlive(topo)
				runtime.KeepAlive(index)
			}
			b.ReportMetric(graphBytes/float64(b.N)/(1<<20), "graph-MiB")
			b.ReportMetric(indexBytes/float64(b.N)/(1<<20), "index-MiB")
			b.ReportMetric(float64(nodes), "graphnodes")
			b.ReportMetric(float64(edges), "graphedges")
		})
	}
}

// BenchmarkSharedRelationshipBuild is the rebuild the broadcaster runs on
// every topology broadcast while a client is connected, with the server's
// options (Secrets included), on both fixtures. The broadcaster keeps the
// previous graph, and the lookup index a relationship read built over it,
// until the new graph replaces them, so each build here runs with a graph and
// an index of that size still live, as the collector sees it there. Building
// the new index is not timed: the broadcaster does that on the next
// relationship read (BenchmarkRelationshipGraphRetained sizes it).
func BenchmarkSharedRelationshipBuild(b *testing.B) {
	for _, f := range relationshipFixtures {
		b.Run(f.name, func(b *testing.B) {
			builder := NewBuilder(newSyntheticProvider(fieldShape().fraction(f.num, f.den)))
			opts := serverRelationshipOptions()
			held, err := builder.Build(opts)
			if err != nil {
				b.Fatalf("build: %v", err)
			}
			heldIndex := IndexByResource(held)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := builder.Build(opts); err != nil {
					b.Fatalf("build: %v", err)
				}
			}
			runtime.KeepAlive(held)
			runtime.KeepAlive(heldIndex)
		})
	}
}

// drawerSubjects are typical drawer reads on the field census: a workload, one
// of its Pods, the Service in front of it, and a ConfigMap and Secret workloads
// reference.
func drawerSubjects(p *mockProvider) []struct {
	name, kind, namespace, object string
	obj                           any
} {
	d, pod, svc, cm, secret := p.deployments[0], p.pods[0], p.services[0], p.configMaps[0], p.secrets[0]
	return []struct {
		name, kind, namespace, object string
		obj                           any
	}{
		{"Deployment", "Deployment", d.Namespace, d.Name, d},
		{"Pod", "Pod", pod.Namespace, pod.Name, pod},
		{"Service", "Service", svc.Namespace, svc.Name, svc},
		{"ConfigMap", "ConfigMap", cm.Namespace, cm.Name, cm},
		{"Secret", "Secret", secret.Namespace, secret.Name, secret},
	}
}

// BenchmarkDrawerRelationships is one drawer's Related Resources read from the
// shared graph, two ways:
//   - shared-index: auth off, the graph's cached index answers the lookup;
//   - per-user-copy: auth on, where the server copies the whole graph, strips
//     the nodes in namespaces the user may not list and rebuilds the index for
//     that one request (relationshipTopologyForUser and cloneTopology in
//     internal/server). The user here may list half the namespaces and every
//     cluster-scoped kind; a user denied some of those also has them stripped
//     before the index is rebuilt.
func BenchmarkDrawerRelationships(b *testing.B) {
	shape := fieldShape()
	provider := newSyntheticProvider(shape)
	topo, err := NewBuilder(provider).Build(serverRelationshipOptions())
	if err != nil {
		b.Fatalf("build: %v", err)
	}
	index := IndexByResource(topo)
	names := synthNames{shape: shape}
	var allowed []string
	for i := 0; i < shape.Namespaces; i += 2 {
		allowed = append(allowed, names.namespace(i))
	}
	for _, s := range drawerSubjects(provider) {
		b.Run("shared-index/"+s.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = GetRelationshipsWithObject(s.kind, s.namespace, s.object, s.obj, topo, provider, nil, index)
			}
		})
	}
	b.Run("per-user-copy/Deployment", func(b *testing.B) {
		s := drawerSubjects(provider)[0]
		b.ReportAllocs()
		for b.Loop() {
			copied := *topo
			copied.Nodes = append([]Node(nil), topo.Nodes...)
			copied.Edges = append([]Edge(nil), topo.Edges...)
			denied := map[string]bool{}
			for _, n := range copied.Nodes {
				if ns, _ := n.Data["namespace"].(string); ns != "" && !slices.Contains(allowed, ns) {
					denied[n.ID] = true
				}
			}
			copied.StripNodeIDs(denied)
			_ = GetRelationshipsWithObject(s.kind, s.namespace, s.object, s.obj, &copied, provider, nil, nil)
		}
	})
}

// BenchmarkSummaryRelationshipsPage reads relationships from an already built
// shared graph and index for every row of a 1,000-row Pod list. Production
// asks only for rows whose controller chain has no readable top controller, so
// a typical page reads far fewer; but a page that asks while the graph is
// stale also waits for the rebuild and the index (see the benchmarks above).
func BenchmarkSummaryRelationshipsPage(b *testing.B) {
	provider := newSyntheticProvider(fieldShape())
	topo, err := NewBuilder(provider).Build(serverRelationshipOptions())
	if err != nil {
		b.Fatalf("build: %v", err)
	}
	index := IndexByResource(topo)
	rows := provider.pods[:1000]
	b.ReportAllocs()
	for b.Loop() {
		for _, pod := range rows {
			_ = GetRelationshipsWithObject("Pod", pod.Namespace, pod.Name, pod, topo, provider, nil, index)
		}
	}
	b.ReportMetric(float64(len(rows)), "rows")
}
