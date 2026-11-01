package meaningfulchanges

import (
	"context"
	"fmt"
	"slices"
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

// runtimeGeneratedKinds are custom kinds controllers create and delete as
// routine bookkeeping without owner references, so their lifecycle says
// nothing about an incident. Their desired-state edits still count.
var runtimeGeneratedKinds = map[string]bool{
	// Cilium allocates identities and endpoints as pods come and go.
	"CiliumIdentity": true, "CiliumEndpoint": true, "CiliumEndpointSlice": true,
	// Calico IPAM allocations.
	"IPAMBlock": true, "IPAMHandle": true, "BlockAffinity": true,
	// Policy engine reports and requests.
	"PolicyReport": true, "ClusterPolicyReport": true, "EphemeralReport": true, "ClusterEphemeralReport": true, "UpdateRequest": true,
	// Provisioned per snapshot.
	"VolumeSnapshotContent": true,
}

// OmittedKinds returns the kinds outside the default feed that had changes in
// the query's window and scope which a kind= query would list: desired-state
// edits to Nodes, and desired-state edits, creates and deletes of untracked
// custom resources. Rows owned by another object (derived custom resources
// such as CiliumEndpoints) and status-only changes are left out, so routine
// churn names nothing; so do creates and deletes of runtimeGeneratedKinds.
// Kinds with desired-state edits come first, then by how many rows they had.
// scanFull reports that the window held more rows than the scan read, so
// other kinds may be missing. q must be a default query (no kind or name).
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
	edits, lifecycle := map[string]int{}, map[string]int{}
	for _, e := range events {
		category := omittedCategory(e)
		if category == "" {
			continue
		}
		change := fromEvent(e, 1)
		if q.Allow != nil && !q.Allow(change) {
			continue
		}
		if category == issuesapi.ChangeCategorySpecConfig {
			edits[e.Kind]++
		} else {
			lifecycle[e.Kind]++
		}
	}
	for _, counts := range []map[string]int{edits, lifecycle} {
		for kind := range counts {
			kinds = append(kinds, kind)
		}
	}
	kinds = slices.Compact(slices.SortedFunc(slices.Values(kinds), strings.Compare))
	sort.SliceStable(kinds, func(i, j int) bool {
		a, b := kinds[i], kinds[j]
		if (edits[a] > 0) != (edits[b] > 0) {
			return edits[a] > 0
		}
		return edits[a]+lifecycle[a] > edits[b]+lifecycle[b]
	})
	return kinds, scanFull, nil
}

// omittedCategory returns the category an explicit kind= query would list the
// row under when the default feed leaves it out and the hint counts it, or "".
func omittedCategory(e timeline.TimelineEvent) issuesapi.ChangeCategory {
	if e.IsManaged() {
		return ""
	}
	category, _ := classify(e)
	switch {
	case category == issuesapi.ChangeCategorySpecConfig:
		if (e.Kind == "Node" && e.Namespace == "") || isUntrackedCustomResource(e) {
			return category
		}
	case category == issuesapi.ChangeCategoryLifecycle:
		if isUntrackedCustomResource(e) && !runtimeGeneratedKinds[e.Kind] {
			return category
		}
	}
	return ""
}

func isUntrackedCustomResource(e timeline.TimelineEvent) bool {
	return isCustomResourceEvent(e) && !(TrackedKind(e.Kind) && eventGroupMatchesTracked(e.Kind, e.APIVersion))
}

// OmittedKindsGuidance is the narrowHint sentence for OmittedKinds' result:
// the kinds it named, or, when the scan was cut short and named none, that
// the check was partial. It is "" when a complete scan named no kind.
func OmittedKindsGuidance(kinds []string, scanFull bool) string {
	if len(kinds) == 0 {
		if !scanFull {
			return ""
		}
		return fmt.Sprintf("Radar checked only the newest %d changes in this window for kinds the default feed leaves out (Nodes, untracked custom kinds) and found none; pass kind=<Kind> to check one.", omittedKindsScanBound)
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

// OmittedKindsErrorGuidance is the narrowHint sentence when the omitted-kinds
// scan failed.
func OmittedKindsErrorGuidance(err error) string {
	return "Radar could not check for changes to kinds the default feed leaves out (" + err.Error() + "); pass kind=<Kind> to check one."
}
