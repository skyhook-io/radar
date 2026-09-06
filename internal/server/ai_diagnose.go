package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/ai"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/checks"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// canonicalDiagnoseTarget resolves the exact Kubernetes identity once, at the
// run boundary. The UI and CLI may use aliases/plural resource names, and an
// omitted group conventionally means the built-in kind. Persisting those raw
// inputs would make a Deployment look unrelated to its own `apps/v1` evidence
// and could collide with same-named CRDs after restart.
func canonicalDiagnoseTarget(ctx context.Context, kind, group, namespace, name string) (string, string) {
	canonicalKind := strings.TrimSpace(kind)
	canonicalGroup := strings.ToLower(strings.TrimSpace(group))
	if canonicalGroup == "" {
		if gvr, ok := k8s.BuiltinGVRAnyGroup(canonicalKind); ok {
			canonicalKind, _ = k8s.BuiltinKindForResource(gvr.Resource)
			canonicalGroup = gvr.Group
		}
	} else if gvr, ok := k8s.BuiltinGVR(canonicalKind, canonicalGroup); ok {
		canonicalKind, _ = k8s.BuiltinKindForResource(gvr.Resource)
		canonicalGroup = gvr.Group
	}
	if cache := k8s.GetResourceCache(); cache != nil {
		if obj, err := cache.GetDynamicWithGroup(ctx, canonicalKind, namespace, name, canonicalGroup); err == nil && obj != nil {
			gvk := obj.GroupVersionKind()
			if gvk.Kind != "" {
				canonicalKind = gvk.Kind
			}
			canonicalGroup = gvk.Group
			return canonicalKind, canonicalGroup
		}
	}

	if discovery := k8s.GetResourceDiscovery(); discovery != nil {
		if canonicalGroup != "" {
			if resource, ok := discovery.GetResourceWithGroup(canonicalKind, canonicalGroup); ok {
				return resource.Kind, resource.Group
			}
		} else if resource, ok := discovery.GetResource(canonicalKind); ok {
			canonicalKind = resource.Kind
			return canonicalKind, resource.Group
		}
	}
	return canonicalKind, canonicalGroup
}

// detectManagedBy reports which GitOps/Helm controller owns the target resource
// (or "" if none), from the resource's own labels/annotations — the markers Argo,
// Flux, and Helm stamp on what they manage. Used to warn before an Apply that a
// direct change will be reverted on the next reconcile. Best effort: a fetch miss
// or unknown kind yields "" (no warning), never an error to the caller.
func (s *Server) detectManagedBy(ctx context.Context, kind, group, namespace, name string) string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return ""
	}
	obj, err := cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
	if err != nil || obj == nil {
		return ""
	}
	return managedByFromMeta(obj)
}

func (s *Server) detectDiagnoseHealth(r *http.Request, kind, group, namespace, name string) *ai.ResourceHealthSignal {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	obj, err := cache.GetDynamicWithGroup(r.Context(), kind, namespace, name, group)
	if err != nil || obj == nil {
		return nil
	}
	gvk := obj.GroupVersionKind()
	canonicalKind := gvk.Kind
	if canonicalKind == "" {
		canonicalKind = kind
	}
	issueSum, issueRows := computeIssueSummaryAndRows(cache, s.issueClusterScopedAccess(r), s.issueRelatedResourceAccess(r), gvk.Group, canonicalKind, namespace, name)
	auditSum, auditRows := computeAuditSummaryAndRows(cache, gvk.Group, canonicalKind, namespace, name)

	var issueCount int
	signal := &ai.ResourceHealthSignal{}
	if issueSum != nil {
		issueCount = issueSum.Count
		signal.IssueCount = issueSum.Count
		signal.HighestSeverity = issueSum.HighestSeverity
		signal.TopReason = issueSum.TopReason
		for _, row := range issueRows[:min(len(issueRows), maxHealthLines)] {
			signal.Issues = append(signal.Issues, ai.HealthLine{
				Severity: string(row.Severity),
				Reason:   row.Reason,
				Message:  capHealthMessage(row.Message),
			})
		}
	}
	if summary := resourcecontext.BuildSummary(obj, resourcecontext.SummaryOptions{IssueCount: issueCount}); summary != nil {
		signal.Health = summary.Health
	}
	if auditSum != nil {
		signal.AuditCount = auditSum.Count
		signal.AuditSeverity = auditSum.HighestSeverity
		signal.TopFinding = auditSum.TopFinding
		for _, row := range auditRows[:min(len(auditRows), maxHealthAuditLines)] {
			signal.AuditFindings = append(signal.AuditFindings, ai.HealthLine{
				Severity: string(checks.MapSeverity(row.Severity)),
				Reason:   row.CheckID,
				Message:  capHealthMessage(row.Message),
			})
		}
	}
	return signal
}

const (
	// maxHealthLines / maxHealthAuditLines cap the rows carried on the health
	// frame: enough to make the context card + prompt concrete, small enough to
	// stay a frame rather than a report (the agent reads the full state itself).
	maxHealthLines      = 3
	maxHealthAuditLines = 2
)

func capHealthMessage(msg string) string {
	const max = 220
	if len(msg) <= max {
		return msg
	}
	return msg[:max-1] + "…"
}

func managedByFromMeta(obj *unstructured.Unstructured) string {
	labels, ann := obj.GetLabels(), obj.GetAnnotations()
	has := func(m map[string]string, k string) bool { _, ok := m[k]; return ok }
	switch {
	// Argo first (it can own a Helm-installed chart); then Flux (it owns the
	// HelmRelease even when Helm stamped the object), then plain Helm.
	case has(ann, "argocd.argoproj.io/tracking-id") || has(labels, "argocd.argoproj.io/instance"):
		return "Argo CD"
	case has(labels, "kustomize.toolkit.fluxcd.io/name") || has(labels, "helm.toolkit.fluxcd.io/name"):
		return "Flux"
	case has(ann, "meta.helm.sh/release-name") || labels["app.kubernetes.io/managed-by"] == "Helm":
		return "Helm"
	}
	return ""
}

// currentConsents reports whether the CURRENT disclosure version has been
// acknowledged, per surface (versions live in internal/config — one source of
// truth with the CLI). Machine-scoped (~/.radar/config.json): one
// acknowledgment covers the web panel and the CLI.
func currentConsents() map[string]bool {
	consents := make(map[string]bool)
	for _, surface := range ai.AllConsentSurfaces() {
		consents[surface] = config.AIConsentGiven(surface)
	}
	return consents
}

// handleListAgents reports local agent CLIs for the OSS "AI Agent" picker.
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	// Version probing is slow (execs `<cli> --version`); only do it when asked
	// (e.g. a settings/picker view) so the Diagnose button's check stays instant.
	withVersions := r.URL.Query().Get("versions") == "1"
	var agents []ai.AgentInfo
	if s.aiDiagnoser != nil {
		// The initialized Diagnoser is authoritative when an explicit CLI override
		// narrows the backend set or points at a binary outside PATH.
		agents = ai.DetectAgents(r.Context(), false)
		agents = mergeDetectedWithDrivable(agents, s.aiDiagnoser.AgentInfos(r.Context(), withVersions))
	} else {
		agents = ai.DetectAgents(r.Context(), withVersions)
	}
	// eligible: this run mode supports local BYO-agent investigations (no proxy/OIDC
	// auth, /mcp mounted) — the SAME gate the boot-time engine init uses. It's true
	// even when no agent is installed, so the UI can distinguish "install an agent
	// to enable this" (eligible && !enabled) from "not available in this deployment"
	// (auth/cloud/--no-mcp), where nudging an install wouldn't help.
	eligible := !s.authConfig.Enabled() && s.mcpHandler != nil
	s.writeJSON(w, map[string]any{
		"agents":    agents,
		"enabled":   s.aiRuns != nil,
		"eligible":  eligible,
		"consented": currentConsents(),
	})
}

func mergeDetectedWithDrivable(detected, drivable []ai.AgentInfo) []ai.AgentInfo {
	byName := make(map[string]ai.AgentInfo, len(drivable))
	for _, info := range drivable {
		byName[info.Name] = info
	}
	seen := make(map[string]bool, len(detected))
	for i := range detected {
		seen[detected[i].Name] = true
		if actual, ok := byName[detected[i].Name]; ok {
			detected[i] = actual
		} else {
			detected[i].Supported = false
			detected[i].Profiles = nil
			detected[i].ConsentSurfaces = nil
		}
	}
	for _, actual := range drivable {
		if !seen[actual.Name] {
			detected = append(detected, actual)
		}
	}
	return detected
}

// handleDiagnoseConsent records the user's acknowledgment of the current
// disclosure for an execution profile. Doesn't require a connected cluster — consent can be
// given while Radar is still connecting.
func (s *Server) handleDiagnoseConsent(w http.ResponseWriter, r *http.Request) {
	if !localOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if s.aiRuns == nil {
		s.writeError(w, http.StatusNotImplemented, "AI investigations are not available")
		return
	}
	var body struct {
		Surface string `json:"surface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if config.AIConsentVersion(body.Surface) == "" {
		s.writeError(w, http.StatusBadRequest, "unknown AI consent surface")
		return
	}
	if err := config.RecordAIConsent(body.Surface); err != nil {
		s.writeError(w, http.StatusInternalServerError, "couldn't record consent: "+err.Error())
		return
	}
	s.writeJSON(w, map[string]any{"ok": true, "consented": currentConsents()})
}

// aiReady gates every diagnose endpoint: the engine is built only in no-auth
// standalone radar with /mcp mounted and an agent CLI present. Returns false (and
// writes the error) when unavailable.
func (s *Server) aiReady(w http.ResponseWriter) bool {
	if s.aiRuns == nil {
		s.writeError(w, http.StatusNotImplemented, "no agent CLI available — install Claude Code, Codex, or Cursor (cursor-agent) to enable AI investigations")
		return false
	}
	return s.requireConnected(w)
}

// validReasoningEffort allows the empty (default) value or one of Codex's
// reasoning-effort levels — never an arbitrary string passed into CLI config.
func validReasoningEffort(e string) bool {
	switch e {
	case "", "minimal", "low", "medium", "high":
		return true
	}
	return false
}

// localOriginOK rejects cross-origin POSTs to these state-changing, process-
// spawning endpoints. Same-origin (no Origin header) passes; otherwise the Origin
// must parse to an exact loopback host — substring checks would let
// "localhost.evil.com" through.
func localOriginOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // same-origin / non-browser
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// handleDiagnoseStart begins an investigation (or focuses a live one for the same
// target) and returns its run id. POST {kind, namespace, name}.
func (s *Server) handleDiagnoseStart(w http.ResponseWriter, r *http.Request) {
	if !localOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if !s.aiReady(w) {
		return
	}
	var body struct {
		Kind, Namespace, Name string
		Group                 string `json:"group"`
		Agent                 string `json:"agent"`
		Profile               string `json:"profile"`
		Model                 string `json:"model"`
		Effort                string `json:"effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	kind := strings.TrimSpace(body.Kind)
	name := strings.TrimSpace(body.Name)
	namespace := strings.TrimSpace(body.Namespace)
	group := strings.ToLower(strings.TrimSpace(body.Group))
	if kind == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "kind and name are required")
		return
	}
	if len(group) > 253 {
		s.writeError(w, http.StatusBadRequest, "API group is too long")
		return
	}
	if namespace != "" {
		if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
			s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
			return
		}
	}
	kind, group = canonicalDiagnoseTarget(r.Context(), kind, group, namespace, name)
	agent := s.aiRuns.AgentName(strings.TrimSpace(body.Agent))
	profile := ai.ExecutionProfile(strings.TrimSpace(body.Profile))
	if profile == "" {
		profile = ai.DefaultProfileFor(agent)
	}
	if !ai.SupportsProfile(agent, profile) {
		s.writeError(w, http.StatusBadRequest, "selected execution profile is not available for this agent")
		return
	}
	// The server owns the consent store, so it enforces it: spawning an agent
	// and shipping cluster data to a model provider must not depend on client
	// code remembering to check. Surface derives from the effective agent and
	// profile because their disclosures can differ.
	if !config.AIConsentGiven(ai.ConsentSurfaceFor(agent, profile)) {
		s.writeError(w, http.StatusForbidden, "AI disclosure not acknowledged for this execution profile — approve the consent prompt first")
		return
	}
	model := strings.TrimSpace(body.Model)
	if len(model) > 100 {
		s.writeError(w, http.StatusBadRequest, "model name too long")
		return
	}
	effort := strings.TrimSpace(body.Effort)
	if !validReasoningEffort(effort) {
		s.writeError(w, http.StatusBadRequest, "invalid reasoning effort")
		return
	}
	// Authoritatively detect whether a GitOps/Helm controller owns this resource, so
	// the Apply confirmation can warn that a direct change will be reverted — rather
	// than relying on the agent to self-report it. Best effort: "" (unknown) on miss.
	managedBy := s.detectManagedBy(r.Context(), kind, group, namespace, name)
	health := s.detectDiagnoseHealth(r, kind, group, namespace, name)
	run, err := s.aiRuns.Start(kind, group, namespace, name, agent, profile, model, effort, managedBy, health)
	if err != nil {
		if errors.Is(err, ai.ErrAtCapacity) {
			s.writeError(w, http.StatusConflict, "too many investigations running — stop or finish one first")
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, run)
}

// handleDiagnoseList returns all retained runs (newest first). historyDegraded
// warns that persistence stopped working, so the UI can say history won't
// survive a restart instead of letting the user believe otherwise.
func (s *Server) handleDiagnoseList(w http.ResponseWriter, r *http.Request) {
	if !s.aiReady(w) {
		return
	}
	s.writeJSON(w, map[string]any{
		"runs":            s.aiRuns.List(),
		"historyDegraded": s.aiRuns.HistoryDegraded(),
	})
}

// handleDiagnoseHistoryClear wipes the persisted investigation history (and
// drops finished runs from memory). Live runs survive. POST, same-origin only.
func (s *Server) handleDiagnoseHistoryClear(w http.ResponseWriter, r *http.Request) {
	if !localOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	// Deliberately NOT aiReady: clearing local history is a disk operation —
	// requiring a connected cluster (like starting a run does) would make the
	// privacy control fail exactly when a user is cleaning up a broken setup.
	if s.aiRuns == nil {
		s.writeError(w, http.StatusNotImplemented, "AI investigations are not available")
		return
	}
	if err := s.aiRuns.ClearHistory(); err != nil {
		s.writeError(w, http.StatusInternalServerError, "couldn't clear history: "+err.Error())
		return
	}
	s.writeJSON(w, map[string]any{"ok": true})
}

// handleDiagnoseTurn adds a follow-up or apply turn to a run. POST {question?,
// apply?, fix?}. Apply enables write tools and binds to the confirmed fix text.
func (s *Server) handleDiagnoseTurn(w http.ResponseWriter, r *http.Request) {
	if !localOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if !s.aiReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var body struct {
		Question          string `json:"question"`
		Apply             bool   `json:"apply"`
		Fix               string `json:"fix"`
		Verify            bool   `json:"verify"`
		ExplainAssessment *int   `json:"explainAssessment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Apply && body.Verify {
		s.writeError(w, http.StatusBadRequest, ai.ErrInvalidTurn.Error())
		return
	}
	if body.Verify && strings.TrimSpace(body.Question) == "" {
		s.writeError(w, http.StatusBadRequest, ai.ErrVerificationQuestionRequired.Error())
		return
	}
	// An apply turn runs in a fresh, write-enabled session bound to the confirmed
	// fix. Without that text the agent would re-derive what to do from live cluster
	// data inside a write session — exactly the injection path fresh-session apply
	// is meant to close. Require the confirmed fix.
	if body.Apply && strings.TrimSpace(body.Fix) == "" {
		s.writeError(w, http.StatusBadRequest, "apply requires the confirmed fix text")
		return
	}
	var err error
	if body.ExplainAssessment != nil {
		if body.Apply || body.Verify || body.Question != "" || body.Fix != "" {
			s.writeError(w, http.StatusBadRequest, "explanation cannot be combined with a question, apply, fix, or verification")
			return
		}
		err = s.aiRuns.AddExplanation(id, *body.ExplainAssessment)
	} else {
		err = s.aiRuns.AddTurn(id, strings.TrimSpace(body.Question), body.Apply, body.Fix, body.Verify)
	}
	switch {
	case errors.Is(err, ai.ErrRunNotFound):
		s.writeError(w, http.StatusNotFound, "investigation not found")
	case errors.Is(err, ai.ErrTurnInFlight):
		s.writeError(w, http.StatusConflict, "a turn is already running")
	case errors.Is(err, ai.ErrNoSession):
		s.writeError(w, http.StatusConflict, "investigation isn't ready for follow-ups yet")
	case err != nil:
		s.writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.writeJSON(w, map[string]any{"ok": true})
	}
}

// handleDiagnoseStop cancels a run's in-flight agent.
func (s *Server) handleDiagnoseStop(w http.ResponseWriter, r *http.Request) {
	if !localOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if !s.aiReady(w) {
		return
	}
	if err := s.aiRuns.Stop(chi.URLParam(r, "id")); err != nil {
		s.writeError(w, http.StatusNotFound, "investigation not found")
		return
	}
	s.writeJSON(w, map[string]any{"ok": true})
}

// handleDiagnoseRunStream streams a run's events over SSE: a replay of everything
// after Last-Event-ID (or ?after=), then the live tail. Disconnecting does NOT
// stop the run — that's the whole point of server-side jobs.
func (s *Server) handleDiagnoseRunStream(w http.ResponseWriter, r *http.Request) {
	// Replaying an existing server-owned run does not require a live cluster.
	// Keeping this endpoint available during a connection outage preserves the
	// durable transcript and lets EventSource reconnect instead of misclassifying
	// a temporary outage as an evicted run.
	if s.aiRuns == nil {
		s.writeError(w, http.StatusNotImplemented, "AI investigations are unavailable")
		return
	}
	run := s.aiRuns.Get(chi.URLParam(r, "id"))
	if run == nil {
		s.writeError(w, http.StatusNotFound, "investigation not found")
		return
	}

	afterSeq := 0
	if le := r.Header.Get("Last-Event-ID"); le != "" {
		if n, err := strconv.Atoi(le); err == nil {
			afterSeq = n
		}
	} else if a := r.URL.Query().Get("after"); a != "" {
		if n, err := strconv.Atoi(a); err == nil {
			afterSeq = n
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Native EventSource does not reliably reconnect after a non-200 or a
	// non-SSE response. Keep hydration failure inside the SSE protocol: this
	// unsequenced, non-durable frame classifies the failure without advancing
	// Last-Event-ID. EOF retries transient reads; permanent corruption is followed
	// by the explicit closed frame below so the consumer stops reconnecting.
	backlog, ch, alreadyFinalized, cancel, err := run.Subscribe(afterSeq)
	if err != nil {
		retryable := !errors.Is(err, ai.ErrHistoryCorrupt)
		sendSSEEvent(w, flusher, "history_unavailable", map[string]any{
			"type":      "history_unavailable",
			"error":     err.Error(),
			"retryable": retryable,
		})
		if !retryable {
			// A valid 200 event-stream EOF normally makes native EventSource retry.
			// Follow the permanent failure with an unsequenced closed frame so the
			// existing consumer stops this EventSource without advancing its durable
			// Last-Event-ID. Reopening the run remains an explicit user action.
			sendSSEEvent(w, flusher, "closed", map[string]string{
				"type":   "closed",
				"reason": "history_unavailable",
			})
		}
		return
	}
	defer cancel()

	send := func(e ai.RunEvent) bool {
		b, err := json.Marshal(e.Event)
		if err != nil {
			return true
		}
		// id: drives EventSource's Last-Event-ID for replay on reconnect.
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Event.Type, b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// This unsequenced, non-persisted marker is the boundary between history and
	// the live tail. It deliberately has no SSE id: Last-Event-ID must continue to
	// point at the last durable run event, so reconnect replay cannot skip data.
	replayComplete := func() {
		sendSSEEvent(w, flusher, "replay_complete", map[string]string{"type": "replay_complete"})
	}
	terminal, ok := sendDiagnoseBacklog(backlog, send, replayComplete)
	if !ok || terminal {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return // client went away — run keeps going server-side
		case e, ok := <-ch:
			if !ok {
				sendDiagnoseClosedIfFinalized(w, flusher, alreadyFinalized)
				return
			}
			if !send(e) {
				return
			}
			if e.Event.Type == "closed" {
				return
			}
		}
	}
}

// sendDiagnoseClosedIfFinalized resolves the two meanings of a closed Run
// subscription. A subscription born after finalization has no future durable
// event to stop EventSource's retry loop, so it gets an unsequenced closed control
// frame. A live subscription can instead close because its bounded buffer filled;
// bare EOF deliberately reconnects and replays the missing durable suffix.
func sendDiagnoseClosedIfFinalized(w http.ResponseWriter, flusher http.Flusher, alreadyFinalized bool) {
	if alreadyFinalized {
		sendSSEEvent(w, flusher, "closed", map[string]string{"type": "closed"})
	}
}

// sendDiagnoseBacklog emits the replay boundary before a retained closed
// sentinel. EventSource consumers close as soon as they receive closed; sending
// the marker afterward would make the explicit replay contract browser-dependent.
func sendDiagnoseBacklog(
	backlog []ai.RunEvent,
	send func(ai.RunEvent) bool,
	replayComplete func(),
) (terminal bool, ok bool) {
	for _, event := range backlog {
		if event.Event.Type == "closed" {
			replayComplete()
			return true, send(event)
		}
		if !send(event) {
			return false, false
		}
	}
	replayComplete()
	return false, true
}
