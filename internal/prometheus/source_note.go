package prometheus

import (
	"slices"

	"github.com/skyhook-io/radar/pkg/prom"
)

func (c *Client) SetSourceNote(src *prom.MetricsSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.retired {
		c.sourceNote = cloneSourceNote(src)
	}
}

func cloneSourceNote(src *prom.MetricsSource) *prom.MetricsSource {
	if src == nil {
		return nil
	}
	copy := *src
	copy.MissingFamilies = slices.Clone(src.MissingFamilies)
	return &copy
}
