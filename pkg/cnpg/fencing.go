package cnpg

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

type FencedInstances struct {
	Raw       string   `json:"raw"`
	All       bool     `json:"all"`
	Instances []string `json:"instances"`
	Malformed bool     `json:"malformed,omitempty"`
}

func ParseFencedInstances(raw string) FencedInstances {
	out := FencedInstances{Raw: raw, Instances: []string{}}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var names []string
	// JSON null parses successfully but is not an operator-accepted list.
	if err := json.Unmarshal([]byte(raw), &names); err != nil || names == nil {
		out.Malformed = true
		return out
	}
	sort.Strings(names)
	out.Instances = slices.Compact(names)
	out.All = slices.Contains(names, "*")
	return out
}

func (f FencedInstances) Fences(instance string) bool {
	return !f.Malformed && (f.All || slices.Contains(f.Instances, instance))
}
