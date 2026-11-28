package kargo

import (
	"cmp"
	"slices"
)

// PipelineStage is a Stage's place in its project's pipeline.
type PipelineStage struct {
	Name string
	// Depth is the Stage's column: 0 for a Stage that takes Freight only
	// from Warehouses, else one more than its deepest upstream Stage.
	Depth int
	// Upstream are the Stages it takes Freight through; Warehouses those it
	// takes Freight straight from.
	Upstream   []string
	Warehouses []string
	// MissingUpstream names upstream Stages that aren't among the Stages
	// given, so the column may be too shallow.
	MissingUpstream []string
}

// Pipeline orders a project's Stages left to right as Kargo draws them,
// from spec.requestedFreight: by depth, then name. A cycle, which Kargo
// doesn't create but a spec can declare, is cut where it closes.
func Pipeline(stages []Stage) []PipelineStage {
	stages = slices.Clone(stages)
	slices.SortFunc(stages, func(a, b Stage) int { return cmp.Compare(a.Name, b.Name) })
	byName := make(map[string]Stage, len(stages))
	for _, s := range stages {
		byName[s.Name] = s
	}
	depth := make(map[string]int, len(stages))
	const visiting = -1
	var visit func(name string) int
	visit = func(name string) int {
		if d, ok := depth[name]; ok {
			if d == visiting {
				return -1
			}
			return d
		}
		depth[name] = visiting
		d := 0
		for _, up := range byName[name].Upstream() {
			if _, ok := byName[up]; !ok {
				continue
			}
			if ud := visit(up); ud >= 0 {
				d = max(d, ud+1)
			}
		}
		depth[name] = d
		return d
	}
	out := make([]PipelineStage, 0, len(stages))
	for _, s := range stages {
		p := PipelineStage{Name: s.Name, Depth: visit(s.Name), Upstream: s.Upstream(), Warehouses: s.Warehouses()}
		for _, up := range p.Upstream {
			if _, ok := byName[up]; !ok {
				p.MissingUpstream = append(p.MissingUpstream, up)
			}
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b PipelineStage) int {
		if c := cmp.Compare(a.Depth, b.Depth); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}
