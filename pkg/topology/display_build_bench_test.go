package topology

import (
	"fmt"
	"testing"
)

// BenchmarkDisplayTopologyBuild measures the builds the SSE broadcaster runs
// for each client group (the default options in each view mode) and the
// relationship-cache options the GitOps tree uses, at census fractions the
// large-cluster guard still draws.
func BenchmarkDisplayTopologyBuild(b *testing.B) {
	traffic := func() BuildOptions { o := DefaultBuildOptions(); o.ViewMode = ViewModeTraffic; return o }
	cases := []struct {
		name string
		opts func() BuildOptions
		dens []int
	}{
		{"resources", DefaultBuildOptions, []int{256}},
		{"traffic", traffic, []int{256}},
		{"relcache", relationshipCacheOptions, []int{256, 64, 16}},
	}
	for _, c := range cases {
		for _, den := range c.dens {
			shape := fieldShape().fraction(1, den)
			b.Run(fmt.Sprintf("%s/1-%d", c.name, den), func(b *testing.B) {
				benchmarkBuild(b, NewBuilder(newSyntheticProvider(shape)), c.opts())
			})
		}
	}
}

// BenchmarkScopedDisplayTopologyBuild measures one namespace of the full field
// census in each view mode: how a large cluster's Topology screen is viewed.
func BenchmarkScopedDisplayTopologyBuild(b *testing.B) {
	shape := fieldShape()
	provider := newSyntheticProvider(shape)
	for _, view := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		b.Run(string(view), func(b *testing.B) {
			opts := DefaultBuildOptions()
			opts.ViewMode = view
			opts.Namespaces = []string{synthNames{shape: shape}.namespace(0)}
			benchmarkBuild(b, NewBuilder(provider), opts)
		})
	}
}

func benchmarkBuild(b *testing.B, builder *Builder, opts BuildOptions) {
	topo, err := builder.Build(opts)
	if err != nil {
		b.Fatalf("build: %v", err)
	}
	if len(topo.Nodes) == 0 {
		b.Fatal("the build drew nothing, so it measures the large-cluster guard, not the graph")
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := builder.Build(opts); err != nil {
			b.Fatalf("build: %v", err)
		}
	}
	b.ReportMetric(float64(len(topo.Nodes)), "graphnodes")
	b.ReportMetric(float64(len(topo.Edges)), "graphedges")
}
