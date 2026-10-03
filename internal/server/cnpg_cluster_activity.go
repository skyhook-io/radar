package server

import (
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/resourceid"
	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
)

const (
	cnpgActivityDefaultWindow = 24 * time.Hour
	cnpgActivityDefaultLimit  = 200
	cnpgActivityMaxLimit      = 1000
	// cnpgActivityScanLimit bounds the rows read to attribute history. A
	// namespace that outgrows it reports truncated rather than silently
	// dropping its oldest attribution.
	cnpgActivityScanLimit = 10000
)

// CNPGClusterActivityResponse is GET /api/cnpg/clusters/{namespace}/{name}/activity.
// Oldest is the earliest row the store still holds for the namespace — the
// floor below which absence means "not retained", not "didn't happen".
// AttributionSince is the earliest visible row that carries this Cluster's
// retained cnpg.io/cluster attribution; before it, deleted children cannot be
// attributed. Both are null when nothing is held.
type CNPGClusterActivityResponse struct {
	Events           []timeline.TimelineEvent `json:"events"`
	Oldest           *time.Time               `json:"oldest"`
	AttributionSince *time.Time               `json:"attributionSince"`
	Truncated        bool                     `json:"truncated"`
}

type cnpgActivityKind struct {
	group, resource string
}

// cnpgActivityKinds are the kinds whose rows can belong to one Cluster: the
// Cluster itself, its instance Pods, and every namespaced CNPG kind.
var cnpgActivityKinds = func() map[string]cnpgActivityKind {
	out := map[string]cnpgActivityKind{"/Pod": {group: "", resource: "pods"}}
	for _, k := range cnpgWorkspaceKinds {
		if !k.clusterScoped {
			out[k.group+"/"+k.kind] = cnpgActivityKind{group: k.group, resource: k.resource}
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
func cnpgRowAttribution(e *timeline.TimelineEvent, name, liveUID string) (matched, labelled bool) {
	group := resourceid.GroupFromAPIVersion(e.APIVersion)
	if _, ok := cnpgActivityKinds[group+"/"+e.Kind]; !ok {
		return false, false
	}
	labelled = e.Labels[pkgtimeline.CNPGClusterLabel] == name
	switch {
	case e.Kind == "Cluster" && group == cnpgGroup:
		return e.Name == name, false
	case e.Kind == "Pod" && group == "":
		o := e.Owner
		owned := o != nil && o.Kind == "Cluster" && o.Name == name && resourceid.GroupFromAPIVersion(o.APIVersion) == cnpgGroup &&
			(liveUID == "" || o.UID == liveUID)
		return owned, owned && labelled
	default:
		return labelled, labelled
	}
}

// handleCNPGClusterActivity serves the Cluster's history from the timeline
// store: the Cluster, its instance Pods, and the CNPG objects attributed to it
// — including ones since deleted — with the K8s Events about each. Rows about
// a kind the caller cannot list in the namespace are dropped.
func (s *Server) handleCNPGClusterActivity(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.requireConnected(w) {
		return
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	if !s.canRead(r, cnpgGroup, "clusters", namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to clusters.postgresql.cnpg.io in namespace "+namespace)
		return
	}

	now := time.Now()
	since := now.Add(-cnpgActivityDefaultWindow)
	if raw := r.URL.Query().Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid since "+strconv.Quote(raw)+" (expected RFC3339)")
			return
		}
		since = t
	}
	var until time.Time
	if raw := r.URL.Query().Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil || !t.After(since) {
			s.writeError(w, http.StatusBadRequest, "invalid until "+strconv.Quote(raw)+" (expected RFC3339 after since)")
			return
		}
		until = t
	}
	limit := cnpgActivityDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			s.writeError(w, http.StatusBadRequest, "invalid limit "+strconv.Quote(raw)+" (expected a positive integer)")
			return
		}
		limit = min(n, cnpgActivityMaxLimit)
	}

	store := timeline.GetStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Timeline store not available")
		return
	}
	clusterContext := k8s.ActiveClusterContext()
	rows, err := store.Query(r.Context(), timeline.QueryOptions{
		Namespaces:       []string{namespace},
		Kinds:            cnpgActivityKindNames(),
		APIGroups:        []string{"", cnpgGroup, cnpgBarmanGroup},
		ClusterContext:   clusterContext,
		IncludeManaged:   true,
		IncludeK8sEvents: true,
		Limit:            cnpgActivityScanLimit,
	})
	if err != nil {
		log.Printf("[cnpg] Failed to query activity for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var liveUID string
	if cache := k8s.GetResourceCache(); cache != nil {
		if live, err := findCNPGCluster(r.Context(), cache, namespace, name); err == nil && live != nil {
			liveUID = string(live.GetUID())
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
		if matched[i] && rows[i].UID != "" {
			attributedUIDs[rows[i].UID] = true
		}
	}

	allowed := map[string]bool{}
	var eventsAllowed *bool
	canList := func(e *timeline.TimelineEvent) bool {
		if e.Source == timeline.SourceK8sEvent {
			if eventsAllowed == nil {
				ok := s.canRead(r, "", "events", namespace, "list")
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
			ok = known && s.canRead(r, target.group, target.resource, namespace, "list")
			allowed[key] = ok
		}
		return ok
	}

	resp := CNPGClusterActivityResponse{Events: []timeline.TimelineEvent{}}
	seenIDs := map[string]bool{}
	var windowed []timeline.TimelineEvent
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
		if e.Timestamp.Before(since) || (!until.IsZero() && e.Timestamp.After(until)) {
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
	resp.Truncated = scanCapped || len(windowed) > limit
	if len(windowed) > limit {
		windowed = windowed[:limit]
	}
	if windowed != nil {
		resp.Events = windowed
	}

	oldest, err := store.Query(r.Context(), timeline.QueryOptions{
		Namespaces:       []string{namespace},
		ClusterContext:   clusterContext,
		IncludeManaged:   true,
		IncludeK8sEvents: true,
		SequenceOrder:    timeline.SequenceOrderAscending,
		Limit:            1,
	})
	if err != nil {
		log.Printf("[cnpg] Failed to query retention floor for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(oldest) > 0 {
		t := oldest[0].Timestamp.UTC()
		resp.Oldest = &t
	}
	s.writeJSON(w, resp)
}
