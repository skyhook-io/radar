package server_test

import (
	"cmp"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// unsearchedEntry compares gap membership only. Extents and output order must be
// asserted on Coverage.Gaps directly.
type unsearchedEntry struct {
	Kind       string
	Group      string
	Reason     readreason.Code
	Namespaces []string
	Count      int
}

func unsearched(res search.Result) []unsearchedEntry {
	out := []unsearchedEntry{}
	for _, gap := range res.Coverage.Gaps {
		switch {
		case gap.Objects != nil:
		case gap.Kinds != nil && gap.Kinds.Extent == "":
			for _, k := range gap.Kinds.Names {
				out = append(out, unsearchedEntry{Kind: k.Kind, Group: k.Group, Reason: gap.Code})
			}
			if more := gap.Kinds.Count - len(gap.Kinds.Names); more > 0 {
				out = append(out, unsearchedEntry{Kind: "*", Reason: gap.Code, Count: more})
			}
		case gap.Namespaces != nil && gap.Namespaces.Extent == "":
			out = append(out, unsearchedEntry{Kind: "*", Reason: gap.Code, Namespaces: gap.Namespaces.Names})
		default:
			out = append(out, unsearchedEntry{Kind: "*", Reason: gap.Code})
		}
	}
	slices.SortFunc(out, func(a, b unsearchedEntry) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Group, b.Group), strings.Compare(string(a.Reason), string(b.Reason)))
	})
	return out
}

func partial(res search.Result) bool { return res.Coverage.Status == resourceapi.CoveragePartial }
