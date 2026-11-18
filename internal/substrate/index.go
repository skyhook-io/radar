package substrate

import (
	"slices"
	"strings"

	model "github.com/skyhook-io/radar/pkg/substrate"
)

// Index holds the projections every request needs, built once per snapshot
// so a request never joins workers against every actor.
type Index struct {
	// ByWorker is each Substrate worker's assigned actors, by worker name.
	ByWorker map[string][]model.Actor
	// Workers are the workers read, by name.
	Workers map[string]model.Worker
	// Newest is the actors ordered newest change first, then by
	// atespace/name: the /actors order.
	Newest []model.Actor
	States model.StateCounts
	// ByAtespace counts actors by state; every atespace with an actor has
	// an entry.
	ByAtespace map[string]*model.StateCounts
	// TemplateUIDs is nil unless templates were read.
	TemplateUIDs model.TemplateUIDs
}

// Index returns the snapshot's projections, built on first use.
func (s *Snapshot) Index() *Index {
	s.indexOnce.Do(func() { s.index = buildIndex(s) })
	return s.index
}

func buildIndex(s *Snapshot) *Index {
	ix := &Index{
		ByWorker:   map[string][]model.Actor{},
		Workers:    make(map[string]model.Worker, len(s.Workers)),
		States:     model.NewStateCounts(),
		ByAtespace: map[string]*model.StateCounts{},
		Newest:     slices.Clone(s.Actors),
	}
	count := func(m map[string]*model.StateCounts, key string, state string) {
		c := m[key]
		if c == nil {
			n := model.NewStateCounts()
			c = &n
			m[key] = c
		}
		c.Add(state)
	}
	for _, a := range s.Actors {
		ix.States.Add(a.State)
		count(ix.ByAtespace, a.Atespace, a.State)
		if as := a.Assignment; as != nil && as.Worker != "" {
			ix.ByWorker[as.Worker] = append(ix.ByWorker[as.Worker], a)
		}
	}
	for _, w := range s.Workers {
		ix.Workers[w.Name] = w
	}
	for _, actors := range ix.ByWorker {
		slices.SortFunc(actors, func(a, b model.Actor) int {
			return strings.Compare(refKey(a.Atespace, a.Name), refKey(b.Atespace, b.Name))
		})
	}
	slices.SortFunc(ix.Newest, func(a, b model.Actor) int {
		if c := b.UpdateTime.Compare(a.UpdateTime); c != 0 {
			return c
		}
		return strings.Compare(refKey(a.Atespace, a.Name), refKey(b.Atespace, b.Name))
	})
	if s.Coverage.Templates.Usable() {
		ix.TemplateUIDs = make(model.TemplateUIDs, len(s.Templates))
		for _, t := range s.Templates {
			ix.TemplateUIDs[t.Ref()] = t.UID
		}
	}
	return ix
}
