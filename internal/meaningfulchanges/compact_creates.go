package meaningfulchanges

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	// lifecycleRankReason is the rank_reason of every plain create or delete
	// row for a tracked config or spec kind.
	lifecycleRankReason = "resource create/delete for config or desired state"

	// createRunMinSize and createRunMaxGap define a run of plain creates worth
	// compacting: at least this many in one namespace, each at most this long
	// after the previous one. A shorter run, or a lone create, keeps its full
	// row. The gap covers the longest pause measured inside one Helm install
	// with hooks (57s).
	createRunMinSize = 5
	createRunMaxGap  = 60 * time.Second
)

// standaloneCreateKinds keep a full row when created, even inside a run: a new
// one changes how existing objects behave (admission, quota, limits, replica
// count, evictions, network access, cross-namespace references) or starts a
// GitOps deploy of its own.
var standaloneCreateKinds = map[string]bool{
	"NetworkPolicy":                  true,
	"ResourceQuota":                  true,
	"LimitRange":                     true,
	"HorizontalPodAutoscaler":        true,
	"PodDisruptionBudget":            true,
	"ReferenceGrant":                 true,
	"MutatingWebhookConfiguration":   true,
	"ValidatingWebhookConfiguration": true,
	"Application":                    true,
	"Kustomization":                  true,
	"HelmRelease":                    true,
}

// CompactCreates moves runs of plain creates out of changes into compact
// per-run lists. A plain create is a row whose only content is the object, its
// creation time and, for a ConfigMap, the workloads that consume it; every
// other field holds the value every such row has. The move is lossless:
// expanding a CreatedObjects entry reproduces each moved row exactly. Rows
// that stay keep their order.
func CompactCreates(changes []issuesapi.RecentChange) ([]issuesapi.RecentChange, []issuesapi.CreatedObjects) {
	type candidate struct {
		index int
		at    time.Time
		zone  string
	}
	byNamespace := map[string][]candidate{}
	for i, change := range changes {
		if !isPlainCreate(change) {
			continue
		}
		at, err := time.Parse(time.RFC3339, change.Timestamp)
		if err != nil {
			continue
		}
		byNamespace[change.Namespace] = append(byNamespace[change.Namespace], candidate{index: i, at: at, zone: zoneSuffix(change.Timestamp)})
	}

	// One apiVersion per kind per entry: a kind whose plain creates in a
	// namespace disagree on apiVersion (including recorded vs not) keeps
	// full rows, so the entry's map never misstates a member.
	for namespace, candidates := range byNamespace {
		versions := map[string]map[string]bool{}
		for _, c := range candidates {
			change := changes[c.index]
			if versions[change.Kind] == nil {
				versions[change.Kind] = map[string]bool{}
			}
			versions[change.Kind][change.APIVersion] = true
		}
		kept := candidates[:0]
		for _, c := range candidates {
			if len(versions[changes[c.index].Kind]) == 1 {
				kept = append(kept, c)
			}
		}
		byNamespace[namespace] = kept
	}

	moved := make([]bool, len(changes))
	var created []issuesapi.CreatedObjects
	for _, candidates := range byNamespace {
		sort.SliceStable(candidates, func(i, j int) bool {
			if !candidates[i].at.Equal(candidates[j].at) {
				return candidates[i].at.Before(candidates[j].at)
			}
			return changeKey(changes[candidates[i].index]) < changeKey(changes[candidates[j].index])
		})
		addRun := func(run []candidate) {
			if len(run) < createRunMinSize {
				return
			}
			first := changes[run[0].index]
			entry := issuesapi.CreatedObjects{
				Namespace: first.Namespace,
				First:     first.Timestamp,
				Count:     len(run),
				Objects:   make([]string, 0, len(run)),
			}
			for _, member := range run {
				change := changes[member.index]
				if change.APIVersion != "" {
					if entry.APIVersions == nil {
						entry.APIVersions = map[string]string{}
					}
					entry.APIVersions[change.Kind] = change.APIVersion
				}
				entry.Objects = append(entry.Objects, createdObjectLine(change, int(member.at.Sub(run[0].at)/time.Second)))
				moved[member.index] = true
			}
			created = append(created, entry)
		}
		start := 0
		for i := 1; i < len(candidates); i++ {
			// A run breaks on a long gap, and on a zone change (a DST
			// boundary) so every member's timestamp can be rebuilt from
			// First with First's zone suffix.
			if candidates[i].at.Sub(candidates[i-1].at) > createRunMaxGap || candidates[i].zone != candidates[start].zone {
				addRun(candidates[start:i])
				start = i
			}
		}
		addRun(candidates[start:])
	}
	if len(created) == 0 {
		return changes, nil
	}
	sort.Slice(created, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339, created[i].First)
		b, _ := time.Parse(time.RFC3339, created[j].First)
		if !a.Equal(b) {
			return a.Before(b)
		}
		return created[i].Namespace < created[j].Namespace
	})
	rest := make([]issuesapi.RecentChange, 0, len(changes))
	for i, change := range changes {
		if !moved[i] {
			rest = append(rest, change)
		}
	}
	return rest, created
}

// isPlainCreate reports whether a row carries nothing beyond the object's
// identity, creation time and ConfigMap consumers. Cluster-scoped rows and
// standalone kinds always keep a full row.
func isPlainCreate(c issuesapi.RecentChange) bool {
	return c.Source == "" &&
		c.Namespace != "" &&
		c.ChangeType == "add" &&
		c.Summary == "add" &&
		c.ChangeCategory == issuesapi.ChangeCategoryLifecycle &&
		c.RankReason == lifecycleRankReason &&
		!c.ApplicationConfigurationChange &&
		!c.NotLinkedToReturnedIssues &&
		len(c.Fields) == 0 &&
		// The line grammar has no place for a withheld count; a create whose
		// consumers were partly hidden keeps its full row.
		c.ConsumedByWithheld == nil &&
		!standaloneCreateKinds[c.Kind] &&
		createdObjectNamePlain(c.Kind) &&
		createdObjectNamePlain(c.Name)
}

// createdObjectNamePlain guards the line grammar: Kubernetes kinds and names
// never contain these characters, so a row that does keeps its full form.
func createdObjectNamePlain(s string) bool {
	return s != "" && !strings.ContainsAny(s, " /(),")
}

func createdObjectLine(c issuesapi.RecentChange, offsetSeconds int) string {
	line := fmt.Sprintf("+%ds %s/%s", offsetSeconds, c.Kind, c.Name)
	if len(c.ConsumedBy) > 0 {
		line += " (used by " + strings.Join(c.ConsumedBy, ", ") + ")"
	}
	return line
}

// zoneSuffix returns an RFC3339 timestamp's zone designator ("Z" or "+03:00").
func zoneSuffix(ts string) string {
	if strings.HasSuffix(ts, "Z") {
		return "Z"
	}
	if len(ts) >= 6 {
		return ts[len(ts)-6:]
	}
	return ""
}
