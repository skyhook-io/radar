package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/traffic"
)

// namespaceLookup builds a set for membership tests. A nil input (all-namespace
// access from parseNamespacesForUser) returns nil, which flowVisibleForNamespaces
// treats as "no restriction".
func namespaceLookup(namespaces []string) map[string]bool {
	if namespaces == nil {
		return nil
	}
	set := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		set[ns] = true
	}
	return set
}

// redactPolicyRefs removes, from the plugin's attribution of a flow, every
// policy the caller may not list — a policy's name is a read of that policy,
// and a cluster-wide one is a cluster-scoped read that visibility of the
// flow's namespaces does not imply. The count of removed references is kept
// so the panel can still say a policy was named.
func (s *Server) redactPolicyRefs(r *http.Request, pv *traffic.PolicyVerdict) *traffic.PolicyVerdict {
	if pv == nil {
		return nil
	}
	out := &traffic.PolicyVerdict{}
	keep := func(refs []traffic.PolicyRef, count *int) []traffic.PolicyRef {
		var kept []traffic.PolicyRef
		for _, ref := range refs {
			group, resource, ok := policyRefResource(ref.Kind)
			if ok && s.canRead(r, group, resource, ref.Namespace, "list") {
				kept = append(kept, ref)
			} else if count != nil {
				*count++
			}
		}
		return kept
	}
	out.AllowedBy = keep(pv.AllowedBy, nil)
	out.DeniedBy = keep(pv.DeniedBy, &out.Withheld)
	return out
}

// policyRefResource maps the policy kinds a network plugin attributes flows
// to onto the API resource a caller must be able to list to learn their
// names. A kind Radar does not know is withheld rather than shown.
func policyRefResource(kind string) (group, resource string, ok bool) {
	switch kind {
	case "NetworkPolicy":
		return "networking.k8s.io", "networkpolicies", true
	case "CiliumNetworkPolicy":
		return "cilium.io", "ciliumnetworkpolicies", true
	case "CiliumClusterwideNetworkPolicy":
		return "cilium.io", "ciliumclusterwidenetworkpolicies", true
	}
	return "", "", false
}

// flowVisibleForNamespaces reports whether a flow may be shown to a user whose
// allowed namespaces are `allowed` (nil = all-namespace access). The traffic
// source can only filter by a single namespace or none, so multi-namespace
// users are filtered here: a flow is visible when either endpoint is in an
// allowed namespace, so a user sees traffic to and from services in the
// namespaces they can read. External/empty-namespace endpoints alone don't
// make a flow visible.
func flowVisibleForNamespaces(flow traffic.Flow, allowed map[string]bool) bool {
	if allowed == nil {
		return true
	}
	return (flow.Source.Namespace != "" && allowed[flow.Source.Namespace]) ||
		(flow.Destination.Namespace != "" && allowed[flow.Destination.Namespace])
}

// handleGetTrafficSources returns available traffic sources and recommendations
// GET /api/traffic/sources
func (s *Server) handleGetTrafficSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	response, err := manager.DetectSources(ctx)
	if err != nil {
		log.Printf("[traffic] Error detecting sources: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to detect traffic sources")
		return
	}

	s.writeJSON(w, response)
}

// handleGetTrafficFlows returns aggregated flow data
// GET /api/traffic/flows
func (s *Server) handleGetTrafficFlows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, []any{})
		return
	}
	opts, err := trafficFlowOptions(r, namespaces)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	response, err := manager.GetFlows(ctx, opts)
	if err != nil {
		log.Printf("[traffic] Error getting flows: %v", err)
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	s.writeJSON(w, trafficFlowsPayload(response, s.visibleFlows(r, response.Flows, namespaces, opts)))
}

// trafficFlowSample is how many records the flows response carries for the
// flow list. The graph is built from the aggregation of all of them; sending
// every record as well made the response grow with the cluster — tens of
// megabytes from a large Hubble install — for a list that shows a screenful.
// The list asks /traffic/flows/records for the traffic behind a selection.
const trafficFlowSample = 1000

// trafficRecordsLimit caps a records response at what the flow list shows for
// the sample, so a selection is never the larger payload of the two.
const trafficRecordsLimit = trafficFlowSample

// maxTrafficMatchBytes bounds the decoded selection a records request carries
// in its query string. The UI keeps the encoded request line far below it, for
// the 8 KiB limits of the proxies in front; this guards the server.
const maxTrafficMatchBytes = 16 << 10

// trafficFlowOptions reads the query parameters the flows and records
// endpoints share. namespaces is the caller's resolved namespace scope.
func trafficFlowOptions(r *http.Request, namespaces []string) (traffic.FlowOptions, error) {
	q := r.URL.Query()
	opts := traffic.DefaultFlowOptions()
	opts.Namespaces = namespaces
	if len(namespaces) == 1 {
		opts.Namespace = namespaces[0]
	}
	if sinceStr := q.Get("since"); sinceStr != "" {
		duration, err := time.ParseDuration(sinceStr)
		if err != nil {
			return opts, fmt.Errorf("invalid 'since' duration format: %s (expected format like '5m', '1h')", sinceStr)
		}
		opts.Since = duration
	}
	for _, ns := range strings.Split(q.Get("excludeNamespaces"), ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			opts.ExcludeNamespaces = append(opts.ExcludeNamespaces, ns)
		}
	}
	opts.ExcludeHost = q.Get("excludeHost") == "true"
	return opts, nil
}

// visibleFlows keeps the flows this caller may see and the request did not
// exclude, with policy names the caller may not read removed. The source may
// filter by namespace itself, but this is the check that holds for every
// source; and a source that cannot apply the exclusions has them applied here.
func (s *Server) visibleFlows(r *http.Request, flows []traffic.Flow, namespaces []string, opts traffic.FlowOptions) []traffic.Flow {
	allowed := namespaceLookup(namespaces)
	kept := make([]traffic.Flow, 0, len(flows))
	for _, f := range flows {
		if flowVisibleForNamespaces(f, allowed) && !opts.Excludes(f) && opts.Match.Matches(f) {
			kept = append(kept, f)
		}
	}
	for i := range kept {
		kept[i].PolicyVerdict = s.redactPolicyRefs(r, kept[i].PolicyVerdict)
	}
	return kept
}

// newestFlows returns up to limit flows, newest first.
func newestFlows(flows []traffic.Flow, limit int) []traffic.Flow {
	sorted := slices.Clone(flows)
	slices.SortStableFunc(sorted, func(a, b traffic.Flow) int { return b.LastSeen.Compare(a.LastSeen) })
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	return sorted
}

// trafficFlowsPayload shapes the flows response. Split out so it can be tested
// directly: the payload is hand-built rather than marshalled from a struct, so a
// field the source sets is easy to drop here without anything failing.
func trafficFlowsPayload(response *traffic.FlowsResponse, flows []traffic.Flow) map[string]any {
	result := map[string]any{
		"source":     response.Source,
		"timestamp":  response.Timestamp,
		"flows":      newestFlows(flows, trafficFlowSample),
		"flowsTotal": len(flows),
		"aggregated": traffic.AggregateFlows(flows),
		// L7 responses arrive on their request's edge, caller to callee on the
		// server's port. A client pairing responses with requests needs to know
		// that rather than guess it from which records happen to be present.
		"l7ResponsesCallerOriented": true,
	}
	addFlowsCoverage(result, response)
	addFlowsWarning(result, response, flows)
	return result
}

// addFlowsCoverage carries what the source said about how much of the window
// it covers. It describes what the source returned, not what this user may
// see, so it stays when filtering removes flows: it explains a thin view.
func addFlowsCoverage(result map[string]any, response *traffic.FlowsResponse) {
	if response.CoveredSince != nil {
		result["coveredSince"] = response.CoveredSince
		result["nodeFlowLimit"] = response.NodeFlowLimit
	}
	if response.FlowLimit > 0 {
		result["flowLimit"] = response.FlowLimit
	}
}

func addFlowsWarning(result map[string]any, response *traffic.FlowsResponse, flows []traffic.Flow) {

	// A partial-data warning qualifies the flows it came with. If the namespace
	// filtering above removed all of them, it now qualifies nothing this user can
	// see — and describing the shape of edges they have no access to is both
	// confusing and more than they asked. An incomplete one stays: it means flows
	// may be missing, which may be exactly why this user sees none. A source
	// that returned no flows in the first place is different again: there the
	// warning is the explanation for the empty result, which is what it is for.
	filteredEverythingOut := len(flows) == 0 && len(response.Flows) > 0
	if response.WarningKind == traffic.WarningPartial && filteredEverythingOut {
		return
	}

	if response.Warning != "" {
		result["warning"] = response.Warning
		// The kind has to travel with the warning: the client retries a transient
		// one and must not retry a permanent one, and an absent kind is read as
		// transient — so dropping it here turns a standing explanation into an
		// endless retry loop.
		if response.WarningKind != "" {
			result["warningKind"] = response.WarningKind
		}
	}
}

// handleGetTrafficRecords returns the newest flow records behind one graph
// selection, for the flow list.
// GET /api/traffic/flows/records?match=<FlowMatch JSON>&since=…&excludeNamespaces=…&excludeHost=true
//
// A GET carrying the selection in the query string rather than a POST: a
// read-only Radar Hub proxy refuses every POST, and this is a read. The
// selection names the raw endpoints behind a graph node or edge — the graph
// renames and merges endpoints, so only the client knows which ones a
// selection stands for. It re-queries the source instead of reusing the
// graph's fetch so that a quiet edge on a busy cluster is not crowded out of
// a capped result; the answer is as of this request, not the graph's.
func (s *Server) handleGetTrafficRecords(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("match")
	if len(raw) > maxTrafficMatchBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("selection is larger than %d bytes", maxTrafficMatchBytes))
		return
	}
	var match traffic.FlowMatch
	if err := json.Unmarshal([]byte(raw), &match); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid 'match': "+err.Error())
		return
	}
	if match.Size() == 0 {
		s.writeError(w, http.StatusBadRequest, "'match' must name at least one endpoint or pair")
		return
	}

	if !s.requireConnected(w) {
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, map[string]any{"flows": []traffic.Flow{}, "matched": 0})
		return
	}
	opts, err := trafficFlowOptions(r, namespaces)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	opts.Match = &match

	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}
	response, err := manager.GetFlows(r.Context(), opts)
	if err != nil {
		log.Printf("[traffic] Error getting flow records: %v", err)
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	flows := s.visibleFlows(r, response.Flows, namespaces, opts)
	result := map[string]any{
		"source":                    response.Source,
		"timestamp":                 response.Timestamp,
		"flows":                     newestFlows(flows, trafficRecordsLimit),
		"matched":                   len(flows),
		"l7ResponsesCallerOriented": true,
	}
	addFlowsCoverage(result, response)
	addFlowsWarning(result, response, flows)
	s.writeJSON(w, result)
}

// handleTrafficFlowsStream provides SSE stream of traffic flows
// GET /api/traffic/flows/stream
func (s *Server) handleTrafficFlowsStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	// Enforce per-user namespace access (parseNamespacesForUser intersects the
	// requested ?namespace= with the user's RBAC-allowed namespaces).
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeError(w, http.StatusForbidden, "no namespace access")
		return
	}
	allowed := namespaceLookup(namespaces)

	opts := traffic.FlowOptions{
		Follow: true,
	}
	// The source filters by a single namespace; multi-namespace users are
	// filtered per-flow below.
	if len(namespaces) == 1 {
		opts.Namespace = namespaces[0]
	}

	flowCh, err := manager.StreamFlows(ctx, opts)
	if err != nil {
		log.Printf("[traffic] Error starting flow stream: %v", err)
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	// Send initial connection event
	if _, err := w.Write([]byte("event: connected\ndata: {}\n\n")); err != nil {
		return
	}
	flusher.Flush()

	// Heartbeat ticker
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case flow, ok := <-flowCh:
			if !ok {
				return
			}

			if !flowVisibleForNamespaces(flow, allowed) {
				continue
			}
			flow.PolicyVerdict = s.redactPolicyRefs(r, flow.PolicyVerdict)

			data, err := json.Marshal(flow)
			if err != nil {
				log.Printf("[traffic] Error marshaling flow: %v", err)
				// Notify client of the error
				if _, writeErr := w.Write([]byte("event: error\ndata: {\"error\":\"Failed to serialize flow data\"}\n\n")); writeErr != nil {
					return
				}
				flusher.Flush()
				continue
			}

			if _, err := w.Write([]byte("event: flow\ndata: " + string(data) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()

		case <-heartbeat.C:
			if _, err := w.Write([]byte("event: heartbeat\ndata: {}\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleSetTrafficSource sets the active traffic source
// POST /api/traffic/source
func (s *Server) handleSetTrafficSource(w http.ResponseWriter, r *http.Request) {
	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	var req struct {
		Source string `json:"source"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Source == "" {
		s.writeError(w, http.StatusBadRequest, "Source name required")
		return
	}

	if err := manager.SetActiveSource(req.Source); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{
		"active": req.Source,
	})
}

// handleGetActiveTrafficSource returns the currently active traffic source
// GET /api/traffic/source
func (s *Server) handleGetActiveTrafficSource(w http.ResponseWriter, r *http.Request) {
	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	active := manager.GetActiveSourceName()

	s.writeJSON(w, map[string]string{
		"active": active,
	})
}

// handleTrafficConnect establishes connection to the traffic source
// This may start a port-forward to metrics service if running locally
// POST /api/traffic/connect
func (s *Server) handleTrafficConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	connInfo, err := manager.Connect(ctx)
	if err != nil {
		log.Printf("[traffic] Error connecting: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, connInfo)
}

// handleTrafficConnectionStatus returns current connection status
// GET /api/traffic/connection
func (s *Server) handleTrafficConnectionStatus(w http.ResponseWriter, r *http.Request) {
	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	connInfo := manager.GetConnectionInfo()
	s.writeJSON(w, connInfo)
}
