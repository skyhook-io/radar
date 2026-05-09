package server

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

// NamespaceScopeMode is the closed enum the frontend's `mode` discriminator
// expects. Mirrors the TS union in web/src/api/client.ts; a typo on either
// side breaks the wire contract.
type NamespaceScopeMode string

const (
	NamespaceScopeClusterWide NamespaceScopeMode = "cluster-wide"
	NamespaceScopeNamespace   NamespaceScopeMode = "namespace"
	NamespaceScopeRestricted  NamespaceScopeMode = "restricted"
)

// NamespaceScopeResponse describes this user's namespace-pick state.
//
// The picker is a per-user view filter — it does NOT mutate the shared cache.
// Picking namespaces narrows what THIS user sees on subsequent reads to the
// intersection of (their picks) and (their RBAC-allowed namespaces).
//
//   - Actives is the user's current pick set (empty = "All namespaces", no
//     narrowing).
//   - Mode is "cluster-wide" when no pick is set and the user can list
//     namespaces, "namespace" when one or more picks are in effect, or
//     "restricted" when the user has no cluster-wide list access and hasn't
//     picked any.
//   - AccessibleNamespaces is the picker source — what the user can choose
//     from. Authoritative=false means it's a best-effort short list (the
//     user lacks list-namespace RBAC; other namespaces may exist).
type NamespaceScopeResponse struct {
	Actives              []string           `json:"actives"`
	KubeconfigNamespace  string             `json:"kubeconfigNamespace"`
	Mode                 NamespaceScopeMode `json:"mode"`
	AccessibleNamespaces []string           `json:"accessibleNamespaces"`
	Authoritative        bool               `json:"authoritative"`
	CanClearNamespace    bool               `json:"canClearNamespace"`
}

// nsPreferenceKey builds the per-user, per-context key for nsPreferences.
// Empty username (auth disabled) collapses to a per-context key, matching
// the local single-user expectation.
func nsPreferenceKey(username, contextName string) string {
	return username + "\x00" + contextName
}

// getActiveNamespaceForUser returns this user's namespace picks for the
// current context. Empty/nil means "All namespaces."
func (s *Server) getActiveNamespaceForUser(r *http.Request) []string {
	username := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		username = u.Username
	}
	ctxName := k8s.GetContextName()
	if ctxName == "" {
		return nil
	}
	v, ok := s.nsPreferences.Load(nsPreferenceKey(username, ctxName))
	if !ok {
		return nil
	}
	picks, _ := v.([]string)
	return picks
}

// setActiveNamespaceForUser updates this user's picks for the current context.
// Pass nil/empty to clear (back to "All namespaces").
func (s *Server) setActiveNamespaceForUser(r *http.Request, namespaces []string) {
	username := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		username = u.Username
	}
	ctxName := k8s.GetContextName()
	if ctxName == "" {
		return
	}
	key := nsPreferenceKey(username, ctxName)
	if len(namespaces) == 0 {
		s.nsPreferences.Delete(key)
		return
	}
	// Defensive copy so callers can mutate their input safely after the store.
	stored := append([]string(nil), namespaces...)
	s.nsPreferences.Store(key, stored)
}

// clearAllNamespacePreferences drops every saved pick. Called on context
// switch — picks against the previous cluster's namespaces are meaningless.
func (s *Server) clearAllNamespacePreferences() {
	s.nsPreferences.Range(func(k, _ any) bool {
		s.nsPreferences.Delete(k)
		return true
	})
}

// finalizePostContextSwitch clears all per-user state that referenced the
// previous cluster. Order is load-bearing: callers MUST run this AFTER
// PerformContextSwitch, never before — running it first opens a window
// where an in-flight request repopulates permCache with the OLD cluster's
// SAR results, and those entries (TTL 2m) then authorize NEW cluster
// requests.
func (s *Server) finalizePostContextSwitch() {
	if s.permCache != nil {
		s.permCache.Invalidate()
	}
	k8s.InvalidateUserCapabilitiesCache()
	s.clearAllNamespacePreferences()
}

// loadSavedNamespacePreference seeds the per-user map from settings.json on
// first reach. Only relevant for the no-auth (local single-user) path —
// auth-enabled deploys don't persist picks across pod restarts.
func (s *Server) loadSavedNamespacePreference(r *http.Request) {
	if auth.UserFromContext(r.Context()) != nil {
		return // multi-user: no shared persisted pref
	}
	ctxName := k8s.GetContextName()
	if ctxName == "" {
		return
	}
	key := nsPreferenceKey("", ctxName)
	if _, ok := s.nsPreferences.Load(key); ok {
		return
	}
	saved := settings.Load()
	if saved.ActiveNamespaces == nil {
		return
	}
	if picks := saved.ActiveNamespaces[ctxName]; len(picks) > 0 {
		s.nsPreferences.Store(key, append([]string(nil), picks...))
	}
}

// intersectPicksWithAllowed returns the picks that survive RBAC filtering.
// allowed=nil means cluster-admin / auth-disabled — all picks pass through.
// Returns nil when the input picks are empty (no narrowing in effect).
func intersectPicksWithAllowed(picks, allowed []string) []string {
	if len(picks) == 0 {
		return nil
	}
	if allowed == nil {
		return append([]string(nil), picks...)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, ns := range allowed {
		allowedSet[ns] = struct{}{}
	}
	out := make([]string, 0, len(picks))
	for _, p := range picks {
		if _, ok := allowedSet[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) handleGetNamespaceScope(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	s.loadSavedNamespacePreference(r)
	actives := s.getActiveNamespaceForUser(r)
	kubeNs := k8s.GetContextNamespace()

	// What the SA / kubeconfig identity sees — used as the input set for
	// per-user filtering below. authoritative=true means "we got a real
	// list from the apiserver"; false means "best-effort short list".
	saAccessible, authoritative := k8s.GetAccessibleNamespaces(r.Context())

	// Intersect with the calling user's RBAC-allowed namespaces. For
	// no-auth callers and cluster-admin users, this is a pass-through
	// (returns saAccessible unchanged). For namespace-restricted users,
	// it returns only the namespaces they can list. authoritative drops to
	// false in the restricted case — the picker UI shows the "limited
	// visibility" affordance accordingly.
	namespaces := saAccessible
	if filtered := s.getUserNamespaces(r, saAccessible); filtered != nil {
		namespaces = filtered
		// If the per-user filter shrank the set, the "authoritative" claim
		// no longer applies — we don't know whether namespaces beyond the
		// user's RBAC exist (yes, they do; but the user can't act on them).
		if len(filtered) < len(saAccessible) {
			authoritative = false
		}
	}

	// Drop picks that the user no longer has access to (RBAC changed mid-
	// session). Partial revocation: keep the survivors, only clear the pick
	// entirely when nothing survives. Persist the trimmed set so it doesn't
	// re-trim on every read.
	if len(actives) > 0 {
		survivors := intersectPicksWithAllowed(actives, namespaces)
		if len(survivors) != len(actives) {
			s.setActiveNamespaceForUser(r, survivors)
			actives = survivors
		}
	}

	mode := NamespaceScopeClusterWide
	switch {
	case len(actives) > 0:
		mode = NamespaceScopeNamespace
	case !authoritative:
		mode = NamespaceScopeRestricted
	}

	// canClear reports whether widening back to "All namespaces" is allowed
	// — cluster-wide list access (authoritative) is sufficient; otherwise we
	// require a kubeconfig or --namespace fallback so the UI has something
	// to fall back to.
	canClear := authoritative || k8s.HasNamespaceFallback()

	// Force non-nil slices so the wire shape matches the TS contract
	// (`string[]`, never `null`). A nil []string marshals to JSON null,
	// which fails downstream on `scope.actives.slice()` etc.
	if actives == nil {
		actives = []string{}
	}
	if namespaces == nil {
		namespaces = []string{}
	}

	s.writeJSON(w, NamespaceScopeResponse{
		Actives:              actives,
		KubeconfigNamespace:  kubeNs,
		Mode:                 mode,
		AccessibleNamespaces: namespaces,
		Authoritative:        authoritative,
		CanClearNamespace:    canClear,
	})
}

type setActiveNamespaceRequest struct {
	// Namespaces to focus on. Empty/missing slice clears the pick (= "All
	// namespaces" up to the user's RBAC ceiling).
	Namespaces []string `json:"namespaces"`
}

func (s *Server) handleSetActiveNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	var req setActiveNamespaceRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		log.Printf("[namespace] invalid set-active-namespace body: %v", err)
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Drop empty strings and de-dupe so callers can't smuggle "" into the
	// stored slice (which would be ambiguous with "no pick").
	cleaned := make([]string, 0, len(req.Namespaces))
	seen := make(map[string]struct{}, len(req.Namespaces))
	for _, ns := range req.Namespaces {
		if ns == "" {
			continue
		}
		if _, dup := seen[ns]; dup {
			continue
		}
		seen[ns] = struct{}{}
		cleaned = append(cleaned, ns)
	}

	// Verify the user actually has access to every requested namespace. For
	// namespace-restricted users, picking a namespace they can't see would
	// create a phantom selection that returns nothing — and would be a quiet
	// info-leak (server-side acknowledgement of a namespace's existence).
	// Use the per-user filtered set, not the SA's set.
	if len(cleaned) > 0 {
		filtered := s.getUserNamespaces(r, cleaned)
		if filtered != nil {
			// filtered semantics: nil = no filter (auth off / cluster-admin),
			// empty = denied, populated = allowed.
			allowedSet := make(map[string]struct{}, len(filtered))
			for _, ns := range filtered {
				allowedSet[ns] = struct{}{}
			}
			for _, ns := range cleaned {
				if _, ok := allowedSet[ns]; !ok {
					s.writeError(w, http.StatusForbidden, "no access to namespace "+ns)
					return
				}
			}
		} else {
			// Cluster-admin / auth-disabled: still verify each namespace
			// exists from the SA's view — picking a typo'd namespace should fail.
			accessible, _ := k8s.GetAccessibleNamespaces(r.Context())
			accessibleSet := make(map[string]struct{}, len(accessible))
			for _, ns := range accessible {
				accessibleSet[ns] = struct{}{}
			}
			for _, ns := range cleaned {
				if _, ok := accessibleSet[ns]; !ok {
					s.writeError(w, http.StatusForbidden, "no access to namespace "+ns)
					return
				}
			}
		}
	}

	s.setActiveNamespaceForUser(r, cleaned)

	// Persist the no-auth (single-user) pick across restarts. Auth-enabled
	// deploys skip persistence — it'd require user-keyed storage we don't
	// have. The in-memory pick already took effect, so a persistence failure
	// is non-fatal — we log and continue.
	if auth.UserFromContext(r.Context()) == nil {
		ctxName := k8s.GetContextName()
		if ctxName != "" {
			if _, err := settings.Update(func(st *settings.Settings) {
				if st.ActiveNamespaces == nil {
					st.ActiveNamespaces = map[string][]string{}
				}
				if len(cleaned) == 0 {
					delete(st.ActiveNamespaces, ctxName)
				} else {
					st.ActiveNamespaces[ctxName] = append([]string(nil), cleaned...)
				}
			}); err != nil {
				log.Printf("[namespace] failed to persist namespace pick for context %q: %v", ctxName, err)
			}
		}
	}

	// Return the fresh scope state so the UI can update without a follow-up GET.
	s.handleGetNamespaceScope(w, r)
}
