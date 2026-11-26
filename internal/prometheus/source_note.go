package prometheus

import (
	"slices"
	"sync"

	"github.com/skyhook-io/radar/pkg/prom"
)

var (
	noteMu sync.Mutex
	note   *prom.MetricsSource
)

// SetSourceNote attaches src to the status of a client that is NOT serving
// through a relay, e.g. to explain why a configured OpenObserve connection
// does not serve this context while discovered Prometheus does. A note never
// changes where queries go. nil clears it.
func SetSourceNote(src *prom.MetricsSource) {
	noteMu.Lock()
	defer noteMu.Unlock()
	if src == nil {
		note = nil
		return
	}
	cp := *src
	cp.MissingFamilies = slices.Clone(cp.MissingFamilies)
	note = &cp
}

func sourceNote() *prom.MetricsSource {
	noteMu.Lock()
	defer noteMu.Unlock()
	if note == nil {
		return nil
	}
	cp := *note
	cp.MissingFamilies = slices.Clone(cp.MissingFamilies)
	return &cp
}
