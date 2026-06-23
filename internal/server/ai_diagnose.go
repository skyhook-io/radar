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
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// detectManagedBy reports which GitOps/Helm controller owns the target resource
// (or "" if none), from the resource's own labels/annotations — the markers Argo,
// Flux, and Helm stamp on what they manage. Used to warn before an Apply that a
// direct change will be reverted on the next reconcile. Best effort: a fetch miss
// or unknown kind yields "" (no warning), never an error to the caller.
func (s *Server) detectManagedBy(ctx context.Context, kind, namespace, name string) string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return ""
	}
	obj, err := cache.GetDynamic(ctx, kind, namespace, name)
	if err != nil || obj == nil {
		return ""
	}
	return managedByFromMeta(obj)
}

func (s *Server) detectDiagnoseHealth(ctx context.Context, kind, namespace, name string) *ai.ResourceHealthSignal {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	obj, err := cache.GetDynamic(ctx, kind, namespace, name)
	if err != nil || obj == nil {
		return nil
	}
	gvk := obj.GroupVersionKind()
	canonicalKind := gvk.Kind
	if canonicalKind == "" {
		canonicalKind = kind
	}
	issueSum := computeIssueSummaryForResource(cache, gvk.Group, canonicalKind, namespace, name)
	auditSum := computeAuditSummaryForResource(cache, gvk.Group, canonicalKind, namespace, name)

	var issueCount int
	signal := &ai.ResourceHealthSignal{}
	if issueSum != nil {
		issueCount = issueSum.Count
		signal.IssueCount = issueSum.Count
		signal.HighestSeverity = issueSum.HighestSeverity
		signal.TopReason = issueSum.TopReason
	}
	if summary := resourcecontext.BuildSummary(obj, resourcecontext.SummaryOptions{IssueCount: issueCount}); summary != nil {
		signal.Health = summary.Health
	}
	if auditSum != nil {
		signal.AuditCount = auditSum.Count
		signal.AuditSeverity = auditSum.HighestSeverity
		signal.TopFinding = auditSum.TopFinding
	}
	return signal
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

// handleListAgents reports the local agent CLIs detected on PATH, for the OSS
// "AI Agent" picker. Safe: only fixed known names are probed (see ai.DetectAgents).
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	// Version probing is slow (execs `<cli> --version`); only do it when asked
	// (e.g. a settings/picker view) so the Diagnose button's check stays instant.
	withVersions := r.URL.Query().Get("versions") == "1"
	s.writeJSON(w, map[string]any{
		"agents":  ai.DetectAgents(r.Context(), withVersions),
		"enabled": s.aiRuns != nil,
	})
}

// aiReady gates every diagnose endpoint: the engine is built only in no-auth
// standalone radar with /mcp mounted and an agent CLI present. Returns false (and
// writes the error) when unavailable.
func (s *Server) aiReady(w http.ResponseWriter) bool {
	if s.aiRuns == nil {
		s.writeError(w, http.StatusNotImplemented, "no agent CLI available — install Claude Code or Codex to enable AI diagnosis")
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
		Agent                 string `json:"agent"`
		Isolated              *bool  `json:"isolated"` // pointer: default ISOLATED when omitted
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
	if kind == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "kind and name are required")
		return
	}
	if namespace != "" {
		if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
			s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
			return
		}
	}
	agent := s.aiRuns.AgentName(strings.TrimSpace(body.Agent))
	isolated := body.Isolated == nil || *body.Isolated
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
	managedBy := s.detectManagedBy(r.Context(), kind, namespace, name)
	health := s.detectDiagnoseHealth(r.Context(), kind, namespace, name)
	run, err := s.aiRuns.Start(kind, namespace, name, agent, isolated, model, effort, managedBy, health)
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

// handleDiagnoseList returns all in-memory runs (newest first).
func (s *Server) handleDiagnoseList(w http.ResponseWriter, r *http.Request) {
	if !s.aiReady(w) {
		return
	}
	s.writeJSON(w, map[string]any{"runs": s.aiRuns.List()})
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
		Question string `json:"question"`
		Apply    bool   `json:"apply"`
		Fix      string `json:"fix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
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
	err := s.aiRuns.AddTurn(id, strings.TrimSpace(body.Question), body.Apply, body.Fix)
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
	if !s.aiReady(w) {
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	backlog, ch, cancel := run.Subscribe(afterSeq)
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

	for _, e := range backlog {
		if !send(e) {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return // client went away — run keeps going server-side
		case e, ok := <-ch:
			if !ok {
				return // run terminated; channel closed
			}
			if !send(e) {
				return
			}
		}
	}
}
