package meaningfulchanges

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	// omittedKindsScanBound is how many of the window's newest rows the
	// omitted-kinds scan reads, the same bound the custom-resource scan of an
	// explicit query uses.
	omittedKindsScanBound = 4 * FetchBound
	omittedKindsNamed     = 5
)

// OmittedKinds returns the kinds outside the default feed that had changes in
// the query's window and scope which a kind= query would list: desired-state
// edits to Nodes, and desired-state edits, creates and deletes of untracked
// custom resources. Rows owned by another object (derived custom resources
// such as CiliumEndpoints) and status-only changes are left out, so routine
// churn names nothing. Kinds are ordered by how many rows they had. scanFull
// reports that the window held more rows than the scan read, so other kinds
// may be missing. q must be a default query (no kind or name).
func OmittedKinds(ctx context.Context, q Query) (kinds []string, scanFull bool, err error) {
	store := timeline.GetStore()
	if store == nil {
		return nil, false, timeline.StoreMissingError()
	}
	q = normalizeQuery(q)
	events, err := store.Query(ctx, timeline.QueryOptions{
		Namespaces:     q.Namespaces,
		Since:          q.start,
		Sources:        []timeline.EventSource{timeline.SourceInformer},
		EventTypes:     append(append([]timeline.EventType{}, updateEventTypes...), lifecycleEventTypes...),
		ClusterContext: k8s.ActiveClusterContext(),
		Limit:          omittedKindsScanBound + 1,
		IncludeManaged: false,
	})
	if err != nil {
		return nil, false, err
	}
	if len(events) > omittedKindsScanBound {
		scanFull = true
		events = events[:omittedKindsScanBound]
	}
	counts := map[string]int{}
	for _, e := range events {
		if !omittedFromDefaultFeed(e) {
			continue
		}
		change := fromEvent(e, 1)
		if q.Allow != nil && !q.Allow(change) {
			continue
		}
		counts[e.Kind]++
	}
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if counts[kinds[i]] != counts[kinds[j]] {
			return counts[kinds[i]] > counts[kinds[j]]
		}
		return kinds[i] < kinds[j]
	})
	return kinds, scanFull, nil
}

// omittedFromDefaultFeed reports whether an explicit kind= query would list
// the row although the default feed does not.
func omittedFromDefaultFeed(e timeline.TimelineEvent) bool {
	if e.IsManaged() {
		return false
	}
	category, _ := classify(e)
	switch {
	case e.Kind == "Node" && e.Namespace == "":
		return category == issuesapi.ChangeCategorySpecConfig
	case isCustomResourceEvent(e) && !(TrackedKind(e.Kind) && eventGroupMatchesTracked(e.Kind, e.APIVersion)):
		return category == issuesapi.ChangeCategorySpecConfig || category == issuesapi.ChangeCategoryLifecycle
	}
	return false
}

// OmittedKindsGuidance is the narrowHint sentence for OmittedKinds' result,
// or "" when it names no kind.
func OmittedKindsGuidance(kinds []string, scanFull bool) string {
	if len(kinds) == 0 {
		return ""
	}
	named := kinds
	more := ""
	if len(kinds) > omittedKindsNamed {
		named = kinds[:omittedKindsNamed]
		more = fmt.Sprintf(" and %d more", len(kinds)-omittedKindsNamed)
	}
	sentence := fmt.Sprintf("This window also has changes to kinds the default feed leaves out: %s%s. Pass kind=<Kind> to list them.", strings.Join(named, ", "), more)
	if scanFull {
		sentence += fmt.Sprintf(" Radar checked only the newest %d changes for these, so other kinds may have changes too.", omittedKindsScanBound)
	}
	return sentence
}
