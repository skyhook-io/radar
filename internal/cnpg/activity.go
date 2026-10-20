package cnpg

import (
	"context"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/resourceid"
	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
)

// The scan bounds attribution across retained rows, before the requested window.
// Outgrowing it reports truncation rather than silently dropping older attribution.
const cnpgActivityScanLimit = 10000

type ActivityTimeline interface {
	Query(context.Context, pkgtimeline.QueryOptions) ([]pkgtimeline.TimelineEvent, error)
}

type ActivityOptions struct {
	Since time.Time
	Until time.Time
	Limit int
}

type ActivityReadError struct {
	Operation string
	Err       error
}

func (e *ActivityReadError) Error() string { return e.Err.Error() }
func (e *ActivityReadError) Unwrap() error { return e.Err }

// CNPGClusterActivityResponse is GET /api/cnpg/clusters/{namespace}/{name}/activity.
// Oldest is the earliest row the store still holds for the namespace — the
// floor below which absence means "not retained", not "didn't happen".
// AttributionSince is the earliest visible row that carries this Cluster's
// retained cnpg.io/cluster attribution; before it, deleted children cannot be
// attributed. Both are null when nothing is held.
type CNPGClusterActivityResponse struct {
	Events           []pkgtimeline.TimelineEvent `json:"events"`
	Oldest           *time.Time                  `json:"oldest"`
	AttributionSince *time.Time                  `json:"attributionSince"`
	Truncated        bool                        `json:"truncated"`
}

type cnpgActivityKind struct {
	group, resource string
}

// cnpgActivityKinds are the kinds whose rows can belong to one Cluster: the
// Cluster itself, its instance Pods, and every namespaced CNPG kind.
var cnpgActivityKinds = func() map[string]cnpgActivityKind {
	out := map[string]cnpgActivityKind{"/Pod": {group: "", resource: "pods"}}
	for _, k := range workspaceKinds {
		if !k.ClusterScoped {
			out[k.Group+"/"+k.Kind] = cnpgActivityKind{group: k.Group, resource: k.Resource}
		}
	}
	return out
}()

func cnpgActivityKindNames() []string {
	seen := map[string]bool{}
	var out []string
	for key := range cnpgActivityKinds {
		_, kind, _ := strings.Cut(key, "/")
		if !seen[kind] {
			seen[kind] = true
			out = append(out, kind)
		}
	}
	sort.Strings(out)
	return out
}

// cnpgRowAttribution decides whether a timeline row is about the named
// Cluster. Rows about the Cluster match by identity; instance Pods by their
// controller owner; CNPG children by the retained cnpg.io/cluster label, which
// survives their deletion. liveUID is the UID of the Cluster that exists now
// under this name, or "" when none does; when set, only Pods it controlled
// count, so a previous same-named Cluster's instances don't merge into a
// recreated one's history.
func cnpgRowAttribution(e *pkgtimeline.TimelineEvent, name, liveUID string) (matched, labelled bool) {
	group := resourceid.GroupFromAPIVersion(e.APIVersion)
	if _, ok := cnpgActivityKinds[group+"/"+e.Kind]; !ok {
		return false, false
	}
	labelled = e.Labels[pkgtimeline.CNPGClusterLabel] == name
	switch {
	case e.Kind == "Cluster" && group == Group:
		return e.Name == name, false
	case e.Kind == "Pod" && group == "":
		o := e.Owner
		owned := o != nil && o.Kind == "Cluster" && o.Name == name && resourceid.GroupFromAPIVersion(o.APIVersion) == Group &&
			(liveUID == "" || o.UID == liveUID)
		return owned, false
	default:
		return labelled, labelled
	}
}

func (s *Reader) ClusterActivity(ctx context.Context, store ActivityTimeline, clusterContext, namespace, name string, options ActivityOptions) (*CNPGClusterActivityResponse, error) {
	rows, err := store.Query(ctx, pkgtimeline.QueryOptions{
		Namespaces:       []string{namespace},
		Kinds:            cnpgActivityKindNames(),
		APIGroups:        []string{"", Group, barmanGroup},
		ClusterContext:   clusterContext,
		IncludeManaged:   true,
		IncludeK8sEvents: true,
		Limit:            cnpgActivityScanLimit,
	})
	if err != nil {
		return nil, &ActivityReadError{Operation: "query activity", Err: err}
	}
	var liveUID string
	jobPods := map[string]WorkspacePod{}
	if cache := s.Observations.Cache; cache != nil {
		clusters, err := s.Observations.DynamicList(ctx, cache, "Cluster", Group, namespace)
		live, _ := SelectCluster(clusters, err, namespace, name)
		if live != nil {
			liveUID = string(live.GetUID())
			_, _, jobs, _, _ := s.workspaceReadPods(ctx, cache, []string{namespace}, clusterUIDs([]*unstructured.Unstructured{live}))
			for _, raw := range jobs {
				pod := raw.(WorkspacePod)
				jobPods[string(pod.Metadata.UID)] = pod
			}
		}
	}
	scanCapped := len(rows) >= cnpgActivityScanLimit

	// Attribution is carried by the subject's own rows; K8s Event rows about
	// a subject whose enrichment was already gone carry only its UID.
	attributedUIDs := map[string]bool{}
	matched := make([]bool, len(rows))
	labelled := make([]bool, len(rows))
	for i := range rows {
		matched[i], labelled[i] = cnpgRowAttribution(&rows[i], name, liveUID)
		if !matched[i] && rows[i].Kind == "Pod" && resourceid.GroupFromAPIVersion(rows[i].APIVersion) == "" {
			if pod, ok := jobPods[rows[i].UID]; ok && rows[i].Owner != nil {
				o := rows[i].Owner
				matched[i] = controlledBy(pod.Metadata.OwnerReferences, resourceid.GroupFromAPIVersion(o.APIVersion), o.Kind, o.Name, types.UID(o.UID))
			}
		}
		if matched[i] && rows[i].UID != "" {
			attributedUIDs[rows[i].UID] = true
		}
	}

	allowed := map[string]bool{}
	var eventsAllowed *bool
	canList := func(e *pkgtimeline.TimelineEvent) bool {
		if e.Source == pkgtimeline.SourceK8sEvent {
			if eventsAllowed == nil {
				ok := s.Access.CanRead(ctx, "", "events", namespace, "list")
				eventsAllowed = &ok
			}
			if !*eventsAllowed {
				return false
			}
		}
		key := resourceid.GroupFromAPIVersion(e.APIVersion) + "/" + e.Kind
		ok, seen := allowed[key]
		if !seen {
			target, known := cnpgActivityKinds[key]
			ok = known && s.Access.CanRead(ctx, target.group, target.resource, namespace, "list")
			allowed[key] = ok
		}
		return ok
	}

	resp := CNPGClusterActivityResponse{Events: []pkgtimeline.TimelineEvent{}}
	seenIDs := map[string]bool{}
	var windowed []pkgtimeline.TimelineEvent
	for i := range rows {
		e := &rows[i]
		if !matched[i] && (e.UID == "" || !attributedUIDs[e.UID]) {
			continue
		}
		if !canList(e) || seenIDs[e.ID] {
			continue
		}
		seenIDs[e.ID] = true
		if labelled[i] && (resp.AttributionSince == nil || e.Timestamp.Before(*resp.AttributionSince)) {
			t := e.Timestamp.UTC()
			resp.AttributionSince = &t
		}
		if e.Timestamp.Before(options.Since) || (!options.Until.IsZero() && e.Timestamp.After(options.Until)) {
			continue
		}
		windowed = append(windowed, *e)
	}
	sort.SliceStable(windowed, func(i, j int) bool {
		if !windowed[i].Timestamp.Equal(windowed[j].Timestamp) {
			return windowed[i].Timestamp.After(windowed[j].Timestamp)
		}
		return windowed[i].ID < windowed[j].ID
	})
	resp.Truncated = scanCapped || len(windowed) > options.Limit
	if len(windowed) > options.Limit {
		windowed = windowed[:options.Limit]
	}
	if windowed != nil {
		resp.Events = windowed
	}

	oldest, err := store.Query(ctx, pkgtimeline.QueryOptions{
		Namespaces:       []string{namespace},
		ClusterContext:   clusterContext,
		IncludeManaged:   true,
		IncludeK8sEvents: true,
		SequenceOrder:    pkgtimeline.SequenceOrderAscending,
		Limit:            1,
	})
	if err != nil {
		return nil, &ActivityReadError{Operation: "query retention floor", Err: err}
	}
	if len(oldest) > 0 {
		t := oldest[0].Timestamp.UTC()
		resp.Oldest = &t
	}
	return &resp, nil
}
