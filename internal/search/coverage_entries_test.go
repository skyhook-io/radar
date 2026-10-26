package search

import (
	"cmp"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// UnsearchedKind compares gap membership only. Extents and output order must be
// asserted on Coverage.Gaps directly.
type UnsearchedKind struct {
	Kind       string
	Group      string
	Reason     readreason.Code
	Namespaces []string
	Count      int
}

func unsearched(res Result) []UnsearchedKind {
	out := []UnsearchedKind{}
	for _, gap := range res.Coverage.Gaps {
		switch {
		case gap.Objects != nil:
		case gap.Kinds != nil && gap.Kinds.Extent == "":
			for _, k := range gap.Kinds.Names {
				out = append(out, UnsearchedKind{Kind: k.Kind, Group: k.Group, Reason: gap.Code})
			}
			if more := gap.Kinds.Count - len(gap.Kinds.Names); more > 0 {
				out = append(out, UnsearchedKind{Kind: "*", Reason: gap.Code, Count: more})
			}
		case gap.Namespaces != nil && gap.Namespaces.Extent == "":
			out = append(out, UnsearchedKind{Kind: "*", Reason: gap.Code, Namespaces: gap.Namespaces.Names})
		default:
			out = append(out, UnsearchedKind{Kind: "*", Reason: gap.Code})
		}
	}
	slices.SortFunc(out, func(a, b UnsearchedKind) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Group, b.Group), strings.Compare(string(a.Reason), string(b.Reason)))
	})
	return out
}

func partial(res Result) bool { return res.Coverage.Status == resourceapi.CoveragePartial }
