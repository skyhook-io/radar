package server

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
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
	CacheScoped          bool               `json:"cacheScoped"`
	CacheScopeNamespace  string             `json:"cacheScopeNamespace,omitempty"`
	NamespaceRescope     bool               `json:"namespaceRescope"`
}

// nsPreferenceKey builds the per-user, per-context key for nsPreferences.
// Empty username (auth disabled) collapses to a per-context key, matching
// the local single-user expectation.
func nsPreferenceKey(username, contextName string) string {
	return username + "\x00" + contextName
}

// getActiveNamespaceForUserInContext returns this user's picks together with
// the context they were read under, as one atomic snapshot. Read paths that
// later mutate the pick must commit against the returned ctxName — capturing
// the context in a separate GetContextName() call risks a mismatch if the
// context switches between the two reads.
func (s *Server) getActiveNamespaceForUserInContext(r *http.Request) (string, []string) {
	username := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		username = u.Username
	}
	ctxName := k8s.GetContextName()
	if ctxName == "" {
		return "", nil
	}
	v, ok := s.nsPreferences.Load(nsPreferenceKey(username, ctxName))
	if !ok {
		return ctxName, nil
	}
	picks, _ := v.([]string)
	return ctxName, picks
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
	// An explicit set — including a clear — expresses user intent; burn the
	// configured-seed eligibility so the --namespaces startup list can't
	// override it on a later read.
	s.seededPicks.Store(key, true)
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
	// Under nsPickMu so an in-flight seed can't land between the two map
	// wipes (a seeded preference without its marker would let a later clear
	// re-seed). No caller holds nsPickMu: handleSetActiveNamespace releases
	// it before any k8s operation whose callbacks reach this function.
	s.nsPickMu.Lock()
	defer s.nsPickMu.Unlock()
	s.nsPreferences.Range(func(k, _ any) bool {
		s.nsPreferences.Delete(k)
		return true
	})
	// Seed marks reference the previous cluster's keys; dropping them lets a
	// switch back to the startup context re-apply the configured initial view.
	s.seededPicks.Range(func(k, _ any) bool {
		s.seededPicks.Delete(k)
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
	s.invalidatePostContextSwitchCaches()
	s.clearAllNamespacePreferences()
	// AI investigations are cancelled + staled by the BEFORE-switch hook (see
	// OnBeforeContextSwitch in New) so they can't touch the new cluster.
}

func (s *Server) invalidatePostContextSwitchCaches() {
	if s.permCache != nil {
		s.permCache.Invalidate()
	}
	if s.rbacMemo != nil {
		s.rbacMemo.Invalidate()
	}
	k8s.InvalidateUserCapabilitiesCache()
	clearPackagesCache()
	clearApplicationsCache()
	s.yamlSchemaMu.Lock()
	clear(s.yamlSchemaCache)
	clear(s.yamlSchemaPathCache)
	clear(s.yamlSchemaBundleCache)
	s.yamlSchemaCacheBytes = 0
	s.yamlSchemaMu.Unlock()
	s.vitalsMetrics.clear()
	gitopstree.ResetUnknownKindLogDedup()
}

// loadSavedNamespacePreference seeds the per-user map on first reach.
// Sources, in priority order:
//   - a settings.json pick (no-auth local single-user only) — a remembered
//     narrower choice survives restarts, and a persisted empty entry is the
//     user's explicit "All namespaces" choice, which suppresses every seed
//     below;
//   - the --namespaces startup list (any user, auth included) — each user's
//     session starts on the configured view. Seeded at most once per
//     (user, context) key, so clearing back to "All namespaces" sticks for
//     the rest of the session instead of being re-applied on the next read;
//   - the singular --namespace flag, then the kubeconfig context's namespace
//     (kubectl parity; no-auth only — the kubeconfig identity belongs to the
//     server operator, not to authenticated users). In-memory only: until
//     the user makes an explicit choice, each restart re-reads flag +
//     kubeconfig, so a kubens-style change is followed rather than frozen at
//     first launch.
func (s *Server) loadSavedNamespacePreference(r *http.Request) {
	ctxName := k8s.GetContextName()
	if ctxName == "" {
		return
	}
	username := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		username = u.Username
	}
	key := nsPreferenceKey(username, ctxName)
	if _, ok := s.nsPreferences.Load(key); ok {
		return
	}

	if username == "" {
		saved, err := settings.LoadChecked()
		if err != nil {
			// A failed read is indistinguishable from "never picked" in the
			// zero value — seeding on it could shadow a real saved pick until
			// restart. Skip every source so the next read can self-heal.
			return
		}
		if picks, ok := saved.ActiveNamespaces[ctxName]; ok {
			if len(picks) > 0 {
				// A settings snapshot read outside the lock can be stale
				// against a concurrent POST set+clear; the atomic seed
				// protocol (marker + absent-preference recheck under the
				// lock) rejects it in that case, the same way it guards the
				// configured list. Seeding from disk also burns configured-
				// seed eligibility, so a later prune eviction falls back to
				// All namespaces instead of resurfacing --namespaces.
				s.seedPick(ctxName, key, picks)
			}
			// An empty entry is the user's persisted explicit "All
			// namespaces" choice — no seeding from flags or kubeconfig.
			return
		}
	}
	if configured := k8s.ConfiguredNamespacesForCurrentContext(); len(configured) > 0 {
		s.seedPick(ctxName, key, configured)
		return
	}
	// Auth users don't inherit the operator's kubeconfig namespace, and under
	// --namespace-scope the cache scope already resolves through the saved
	// pick → kubeconfig namespace chain (see GetNamespaceScopeTarget) — a
	// view-filter seed would just shadow that lifecycle.
	if username != "" || k8s.ForceNamespaceScope {
		return
	}
	var seed []string
	if ns := k8s.ConfiguredNamespaceForCurrentContext(); ns != "" {
		seed = []string{ns}
	} else if ns := k8s.GetContextNamespace(); ns != "" {
		seed = []string{ns}
	}
	// Pruned against known namespaces so a stale kubeconfig value can't
	// narrow the view to nothing and then churn through the prune-reseed
	// cycle on every read.
	seed = pruneToExistingNamespaces(seed, s.allNamespaceNames())
	if len(seed) > 0 {
		s.seedPick(ctxName, key, seed)
	}
}

// seedPick installs picks (from settings.json, the --namespaces startup
// list, or the kubeconfig-namespace default) as key's initial pick. The
// whole decision runs under nsPickMu so it cannot interleave with an
// explicit POST (which burns the seed marker, including on clears) or a
// context switch (which clears both maps under the same lock): context,
// marker, and preference are all rechecked inside the critical section. A
// bare preference CAS is NOT enough here — an absent key means both "never
// picked" (seed) and "explicitly cleared" (don't); the marker is what tells
// them apart.
func (s *Server) seedPick(ctxName, key string, picks []string) {
	s.nsPickMu.Lock()
	defer s.nsPickMu.Unlock()
	if k8s.GetContextName() != ctxName {
		return
	}
	if _, considered := s.seededPicks.LoadOrStore(key, true); considered {
		return
	}
	if _, ok := s.nsPreferences.Load(key); ok {
		return
	}
	s.nsPreferences.Store(key, append([]string(nil), picks...))
}

// pruneToExistingNamespaces returns picks minus namespaces absent from
// existing. An empty existing list means the namespace informer can't answer
// (namespace-scoped cache, restricted RBAC) — picks pass through unchanged,
// since wrongly evicting a valid pick is worse than keeping a stale one.
func pruneToExistingNamespaces(picks, existing []string) []string {
	if len(existing) == 0 {
		return picks
	}
	return intersectPicksWithAllowed(picks, existing)
}

// commitPickMutation replaces the active pick with survivors, but only if the
// stored pick is still exactly `expected` under the still-current context.
//
// Every read-path pick mutation (deleted-namespace prune, RBAC trim, empty-
// fallback clear, first-request seed) computes its result from a snapshot read
// earlier in the request. Between that read and the write, a concurrent POST
// can install a fresh pick or a context switch can swap the cluster. Writing
// unconditionally would revert the fresh pick or persist old-context survivors
// under the new context's key — the lost-update class the namespace-pick lock
// exists to close. On a snapshot mismatch the write is skipped: the caller
// still uses its own snapshot for this one request, and the live pick
// re-converges on its next read.
//
// persist rewrites the no-auth settings.json so the mutation survives a restart
// (and isn't re-seeded stale from disk). Only the deleted-namespace prune sets
// it — an RBAC trim, empty-fallback clear, or seed is a per-user in-memory view
// filter that must not touch the shared single-user settings file.
func (s *Server) commitPickMutation(r *http.Request, ctxName string, expected, survivors []string, persist bool) {
	if ctxName == "" {
		return
	}
	username := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		username = u.Username
	}
	key := nsPreferenceKey(username, ctxName)

	s.nsPickMu.Lock()
	defer s.nsPickMu.Unlock()

	// Bind the whole mutation to the snapshot ctxName — never re-derive the
	// key from the live context inside the critical section. A concurrent
	// context switch flips k8s.GetContextName() without holding nsPickMu, so
	// going through get/setActiveNamespaceForUser (which each re-read the live
	// context) could compare against one context's pick and then store under
	// another's. Skip if the context already moved on; otherwise read, compare,
	// and write all under the same key.
	if k8s.GetContextName() != ctxName {
		return
	}
	var current []string
	if v, ok := s.nsPreferences.Load(key); ok {
		current, _ = v.([]string)
	}
	if !slices.Equal(current, expected) {
		return
	}
	if len(survivors) == 0 {
		s.nsPreferences.Delete(key)
	} else {
		// Defensive copy so callers can mutate their input after the store.
		s.nsPreferences.Store(key, append([]string(nil), survivors...))
	}
	if persist && auth.UserFromContext(r.Context()) == nil && !k8s.ForceNamespaceScope {
		if err := persistNamespacePick(ctxName, survivors, false); err != nil {
			log.Printf("[namespace] failed to persist namespace pick for context %q: %v", ctxName, err)
		}
	}
}

// pruneDeletedNamespacePicks drops saved picks whose namespaces were deleted
// from the cluster. Without this, a stale pick silently empties every read —
// in no-auth mode nothing downstream re-validates it (getUserNamespaces is a
// pass-through), so the UI looks like an empty cluster forever.
//
// Survivors are written back to the in-memory pick and, for the no-auth
// single-user case, to settings.json — otherwise loadSavedNamespacePreference
// re-seeds the stale pick from disk on the next request and the eviction is
// undone. Skipped under --namespace-scope, where the saved pick doubles as
// the cache-scope restore value and handleSetActiveNamespace owns its
// lifecycle.
// ctxName is the context the picks were snapshotted under (from
// getActiveNamespaceForUserInContext). The commit binds to it rather than
// re-reading the live context, so a switch between the caller's snapshot and
// this prune can't persist old-context survivors under the new context's key.
func (s *Server) pruneDeletedNamespacePicks(r *http.Request, ctxName string, picks []string) []string {
	survivors := pruneToExistingNamespaces(picks, s.allNamespaceNames())
	// Compare contents, not just length: skip the write only when nothing
	// actually changed. A length check would rely on the prune being a pure
	// order-preserving filter; equality holds regardless of how it evolves.
	if slices.Equal(survivors, picks) {
		return survivors
	}
	s.commitPickMutation(r, ctxName, picks, survivors, true)
	return survivors
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
	// Read the pick and its context as one snapshot so a later trim commits
	// against the same context it was computed from, not a switched-in one.
	pickCtx, activesRaw := s.getActiveNamespaceForUserInContext(r)
	actives := s.pruneDeletedNamespacePicks(r, pickCtx, activesRaw)
	kubeNs := k8s.GetContextNamespace()
	cacheScopeNs := k8s.GetNamespaceScopeTarget()

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
	// entirely when nothing survives. Store the trimmed set so it doesn't
	// re-trim on every read — through the guarded mutation so a stale trim
	// can't revert a concurrent POST or cross a context switch.
	if len(actives) > 0 {
		survivors := intersectPicksWithAllowed(actives, namespaces)
		if len(survivors) != len(actives) {
			s.commitPickMutation(r, pickCtx, actives, survivors, false)
			actives = survivors
		}
	}

	if k8s.ForceNamespaceScope {
		if cacheScopeNs != "" {
			actives = []string{cacheScopeNs}
		}
		if s.authConfig.Enabled() {
			// Only advertise the pinned namespace as accessible to THIS user if
			// they can actually list it. The shared cache holds only cacheScopeNs,
			// but RBAC still gates each user's reads of it — claiming access the
			// read paths then deny would make the picker lie.
			if cacheScopeNs != "" && len(intersectPicksWithAllowed([]string{cacheScopeNs}, namespaces)) > 0 {
				namespaces = []string{cacheScopeNs}
				authoritative = true
			} else {
				actives = []string{}
				namespaces = []string{}
				authoritative = false
			}
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
	if k8s.ForceNamespaceScope {
		canClear = false
	}

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
		CacheScoped:          k8s.ForceNamespaceScope,
		CacheScopeNamespace:  cacheScopeNs,
		NamespaceRescope:     k8s.ForceNamespaceScope && !s.authConfig.Enabled(),
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

	// Forced-scope requests must name exactly one namespace. Validate before any
	// persistence so a malformed (empty / multi) request can't mutate the saved
	// pick and then 400.
	if k8s.ForceNamespaceScope && len(cleaned) != 1 {
		s.writeError(w, http.StatusBadRequest, "--namespace-scope requires exactly one active namespace")
		return
	}

	// Under --namespace-scope the persisted pick and the live cache scope must
	// move as one commit. Serialize the whole section (persist → rescope →
	// re-sync) so two concurrent requests can't persist one namespace while the
	// cache ends on another. (PerformNamespaceRescope's own lock only serializes
	// the rebuild, not this handler's persist.)
	if k8s.ForceNamespaceScope {
		s.scopeMutationMu.Lock()
		defer s.scopeMutationMu.Unlock()
	}
	// Pairs this handler's persist+set with the read-path stale-pick prune:
	// the prune re-checks the live pick under the same lock before mutating,
	// so it can't revert a pick set here from a stale snapshot. Lock order is
	// scopeMutationMu → nsPickMu; the prune takes only nsPickMu.
	//
	// nsPickMu MUST NOT be held across k8s operations: PerformNamespaceRescope
	// waits on the k8s operation lock, and a concurrent context switch holding
	// that lock fires callbacks that take nsPickMu (finalizePostContextSwitch)
	// — holding it there closes an AB/BA deadlock. The rescope branch releases
	// and re-acquires around the k8s call; commitPickMutation's CAS keeps the
	// unlocked window safe against read-path mutations.
	//
	// Released explicitly before the closing handleGetNamespaceScope render:
	// that path runs the prune, which takes nsPickMu itself — holding it
	// across the render would self-deadlock (the mutex is not reentrant).
	// The held-flag closure keeps every early error return covered by the
	// defer across the release/re-acquire cycle.
	s.nsPickMu.Lock()
	nsPickHeld := true
	unlockNsPick := func() {
		if nsPickHeld {
			nsPickHeld = false
			s.nsPickMu.Unlock()
		}
	}
	defer unlockNsPick()

	// Persist the no-auth (single-user) pick across restarts before acting on it.
	// Auth-enabled deploys skip persistence — it'd require user-keyed storage we
	// don't have. Under --namespace-scope a reconnect restores the cache scope
	// from this saved value, so a rescope must NOT proceed if we can't save the
	// pick: the cache would rebuild for the new namespace but snap back to the
	// stale saved one on the next reconnect, diverging from the live override.
	// Outside scope mode the pick is just a view filter, so a save failure is
	// non-fatal — log and continue.
	if auth.UserFromContext(r.Context()) == nil {
		if ctxName := k8s.GetContextName(); ctxName != "" {
			if err := persistNamespacePick(ctxName, cleaned, true); err != nil {
				log.Printf("[namespace] failed to persist namespace pick for context %q: %v", ctxName, err)
				if k8s.ForceNamespaceScope {
					s.writeError(w, http.StatusServiceUnavailable, "failed to save namespace pick: "+err.Error())
					return
				}
			}
		}
	}

	if k8s.ForceNamespaceScope {
		currentScope := k8s.GetNamespaceScopeTarget()
		if s.authConfig.Enabled() {
			if cleaned[0] != currentScope {
				s.writeError(w, http.StatusForbidden, "--namespace-scope locks the shared cache to namespace "+currentScope)
				return
			}
		} else if cleaned[0] != currentScope {
			// A rescope tears down and rebuilds the informer caches for a different
			// namespace. PerformNamespaceRescope stops active sessions itself, but
			// only once it commits to the teardown (after its connectivity check),
			// so a failed rescope doesn't kill port-forwards / exec for nothing.
			unlockNsPick()
			if err := k8s.PerformNamespaceRescope(cleaned[0]); err != nil {
				// We persisted the requested pick above, but the rescope didn't take
				// (rolled back to the previous namespace, or superseded by a newer op).
				// Re-sync the saved pick to whatever scope is actually live now, so a
				// later reconnect doesn't restore the namespace we just rejected.
				if ctxName := k8s.GetContextName(); ctxName != "" {
					var livePick []string
					if live := k8s.GetNamespaceScopeTarget(); live != "" {
						livePick = []string{live}
					}
					if perr := persistNamespacePick(ctxName, livePick, false); perr != nil {
						log.Printf("[namespace] failed to restore saved pick after rescope failure for context %q: %v", ctxName, perr)
					}
				}
				safeNamespace := strings.ReplaceAll(strings.ReplaceAll(cleaned[0], "\n", ""), "\r", "")
				safeErr := strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\n", ""), "\r", "")
				log.Printf("[namespace] failed to rescope cache to namespace %q: %s", safeNamespace, safeErr)
				s.writeError(w, http.StatusServiceUnavailable, "failed to rescope namespace cache: "+err.Error())
				return
			}
			s.finalizePostContextSwitch()
			s.nsPickMu.Lock()
			nsPickHeld = true
		}
	}

	s.setActiveNamespaceForUser(r, cleaned)
	unlockNsPick()

	// Return the fresh scope state so the UI can update without a follow-up GET.
	s.handleGetNamespaceScope(w, r)
}

// persistNamespacePick saves the single-user namespace pick for ctxName so it
// survives restarts and is restored on reconnect.
//
// An empty pick is ambiguous: "the user chose All namespaces" must survive
// restarts (it suppresses the kubeconfig-namespace default in
// loadSavedNamespacePreference), while a system-side prune to empty should
// return the context to that default. explicitAll picks between the two —
// true persists an empty entry as the user's choice, false deletes the entry.
func persistNamespacePick(ctxName string, cleaned []string, explicitAll bool) error {
	_, err := settings.Update(func(st *settings.Settings) {
		if st.ActiveNamespaces == nil {
			st.ActiveNamespaces = map[string][]string{}
		}
		switch {
		case len(cleaned) > 0:
			st.ActiveNamespaces[ctxName] = append([]string(nil), cleaned...)
		case explicitAll:
			// Non-nil so it round-trips as [] rather than null.
			st.ActiveNamespaces[ctxName] = []string{}
		default:
			delete(st.ActiveNamespaces, ctxName)
		}
	})
	return err
}
