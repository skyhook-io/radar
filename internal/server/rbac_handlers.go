package server

import (
	"fmt"
	"log"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	authv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/rbac"
)

// rbacGroup is the API group for RBAC resources; passed to s.canRead for
// the per-user permission probe.
const rbacGroup = "rbac.authorization.k8s.io"

// SubjectResponse is the wire shape returned by /api/rbac/subject/...
// Mirrors rbac.EffectiveRules but with explicit JSON tagging and the
// inherited bindings grouped by group name for the UI.
type SubjectResponse struct {
	Subject             rbac.Subject             `json:"subject"`
	Direct              []BindingRulesJSON       `json:"direct"`
	InheritedFromGroups []InheritedGroupBindings `json:"inheritedFromGroups"`
	Flat                []rbacv1.PolicyRule      `json:"flat"`
	Truncated           bool                     `json:"truncated"`
	// UsedByPods is populated only for ServiceAccount subjects: the list of
	// Pods whose spec.serviceAccountName references this SA. Closes the
	// loop on the SA detail page — "what's *actually running* as this SA".
	// Nil/empty for User and Group subjects (Kubernetes doesn't expose a
	// way to enumerate Pods running as an external identity).
	UsedByPods []PodRef `json:"usedByPods,omitempty"`
}

// PodRef is a compact identity for a Pod, used in usedByPods. We don't
// include status/health here — the UI links into the Pod detail page for
// that, and including it would inflate the payload on SAs used by
// thousands of Pods (e.g. the default SA in a busy namespace).
type PodRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// InheritedGroupBindings groups inherited bindings under the group name
// that brought them in, so the UI can render "Inherited via X (N
// bindings)" as a single collapsible section.
type InheritedGroupBindings struct {
	GroupName string             `json:"groupName"`
	Bindings  []BindingRulesJSON `json:"bindings"`
}

// BindingRulesJSON is the JSON projection of rbac.BindingRules. We
// reshape to avoid leaking InheritedFromGroup at the per-binding level
// (the response already groups by group name; per-binding repetition
// would be noise).
type BindingRulesJSON struct {
	Binding        rbac.BindingRef     `json:"binding"`
	Role           rbac.RoleRef        `json:"role"`
	Rules          []rbacv1.PolicyRule `json:"rules"`
	ScopeNamespace string              `json:"scopeNamespace,omitempty"`
}

// RoleResponse is the wire shape returned by /api/rbac/role/...
type RoleResponse struct {
	Role     rbac.RoleRef          `json:"role"`
	Bindings []BindingWithSubjects `json:"bindings"`
}

// BindingWithSubjects pairs a binding with its subjects for the
// reverse-lookup-on-role view.
type BindingWithSubjects struct {
	Binding  rbac.BindingRef `json:"binding"`
	Subjects []rbac.Subject  `json:"subjects"`
}

// NamespaceRBACResponse summarises every binding that touches a given
// namespace. Used by NamespaceRenderer to answer "what RBAC is configured
// here" without forcing the operator to pivot through individual SAs.
type NamespaceRBACResponse struct {
	Namespace                           string                `json:"namespace"`
	RoleBindings                        []BindingWithSubjects `json:"roleBindings"`
	ClusterRoleBindingsWithLocalSubject []BindingWithSubjects `json:"clusterRoleBindingsWithLocalSubject"`
	ServiceAccountCount                 int                   `json:"serviceAccountCount"`
}

// WhoamiResponse is a minimal projection of SelfSubjectRulesReview.
// We don't pass through the full Status because the K8s types include
// fields the UI doesn't need and would bloat the payload.
type WhoamiResponse struct {
	Namespace        string                   `json:"namespace"`
	ResourceRules    []authv1.ResourceRule    `json:"resourceRules"`
	NonResourceRules []authv1.NonResourceRule `json:"nonResourceRules"`
	Incomplete       bool                     `json:"incomplete"`
	// EvaluationError is set when the apiserver could compute only a
	// partial rule set (e.g. a webhook authorizer timed out). Surface it
	// honestly — the UI shouldn't pretend the list is exhaustive.
	EvaluationError string `json:"evaluationError,omitempty"`
}

// buildRBACIndex builds (or returns a cached) RBAC index from the four
// cached lister snapshots. Returns nil if any required lister is unavailable
// (informers not started, kind disabled). Callers must check.
func (s *Server) buildRBACIndex() *rbac.Index {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	// All four listers must be present — the index is meaningless without
	// the full picture. (E.g. computing effective rules for an SA without
	// ClusterRoleBindings would silently undercount system:authenticated
	// grants.)
	roleLister := cache.Roles()
	clusterRoleLister := cache.ClusterRoles()
	rbLister := cache.RoleBindings()
	crbLister := cache.ClusterRoleBindings()
	if roleLister == nil || clusterRoleLister == nil || rbLister == nil || crbLister == nil {
		return nil
	}

	build := func() *rbac.Index {
		roles, _ := roleLister.List(labels.Everything())
		clusterRoles, _ := clusterRoleLister.List(labels.Everything())
		roleBindings, _ := rbLister.List(labels.Everything())
		clusterRoleBindings, _ := crbLister.List(labels.Everything())
		return rbac.BuildIndex(roles, clusterRoles, roleBindings, clusterRoleBindings)
	}

	if s.rbacMemo != nil {
		return s.rbacMemo.Get(build)
	}
	return build()
}

// requireRBACReadable gates the reverse-lookup endpoints by checking
// the caller can list both RoleBindings and ClusterRoleBindings (the
// load-bearing reads for either subject- or role-keyed lookups). We
// also want Roles + ClusterRoles for rule expansion, but the absence
// of role reads degrades gracefully (rules render as "permission
// hidden") whereas binding-list absence is a security smell — silent
// partial reverse-lookups mislead operators.
//
// Returns false and writes a 403 with a clear message when the user
// can't list bindings.
func (s *Server) requireRBACReadable(w http.ResponseWriter, r *http.Request) bool {
	if !s.canRead(r, rbacGroup, "rolebindings", "", "list") {
		s.writeError(w, http.StatusForbidden,
			"requires list permission on rolebindings (rbac.authorization.k8s.io) to compute reverse-lookup")
		return false
	}
	if !s.canRead(r, rbacGroup, "clusterrolebindings", "", "list") {
		s.writeError(w, http.StatusForbidden,
			"requires list permission on clusterrolebindings (rbac.authorization.k8s.io) to compute reverse-lookup")
		return false
	}
	return true
}

// handleRBACSubject returns the reverse-lookup graph for a single
// subject. Two URL shapes:
//
//	GET /api/rbac/subject/ServiceAccount/{namespace}/{name}
//	GET /api/rbac/subject/{User|Group}/{name}            (no namespace)
//
// chi sees these as separate routes — see RegisterRoutes for the wiring.
func (s *Server) handleRBACSubject(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	if !s.requireRBACReadable(w, r) {
		return
	}

	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")
	namespace := chi.URLParam(r, "namespace") // empty for User/Group route
	if kind == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "kind and name are required")
		return
	}
	if kind != "ServiceAccount" && kind != "User" && kind != "Group" {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported subject kind %q (want ServiceAccount, User, or Group)", kind))
		return
	}
	if kind == "ServiceAccount" && namespace == "" {
		s.writeError(w, http.StatusBadRequest, "ServiceAccount subject requires a namespace")
		return
	}
	if (kind == "User" || kind == "Group") && namespace != "" {
		// Both routes (with and without namespace) target the same handler.
		// User/Group subjects are always keyed with empty namespace in the
		// index, so silently accepting a namespace here would return an
		// empty 200 OK that looks like "no permissions" - reject with a
		// clear 400 instead.
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("%s subject must not have a namespace (use /api/rbac/subject/%s/%s)", kind, kind, name))
		return
	}

	idx := s.buildRBACIndex()
	if idx == nil {
		s.writeError(w, http.StatusServiceUnavailable, "RBAC cache not available (informers not synced or RBAC reads disabled for the Radar SA)")
		return
	}

	subj := rbac.Subject{Kind: kind, Namespace: namespace, Name: name}
	er := idx.EffectiveRules(subj)

	resp := SubjectResponse{
		Subject:             subj,
		Direct:              []BindingRulesJSON{},
		InheritedFromGroups: []InheritedGroupBindings{},
		Flat:                er.Flat,
		Truncated:           er.Truncated,
	}
	// Populating usedByPods requires listing pods in the SA's namespace —
	// gate on the caller's own pod-list permission. Without this, a user
	// with list-rolebindings but no list-pods in (e.g.) kube-system could
	// enumerate pod names by querying SAs there.
	if subj.Kind == "ServiceAccount" && s.canRead(r, "", "pods", subj.Namespace, "list") {
		resp.UsedByPods = podsUsingServiceAccount(subj)
	}
	// Group bindings: directs are returned flat; inherited ones bucket
	// by InheritedFromGroup so the UI can render a single collapsible
	// section per group instead of repeating the group name on every row.
	inheritedByGroup := map[string][]BindingRulesJSON{}
	for _, br := range er.ViaBindings {
		entry := BindingRulesJSON{
			Binding:        br.Binding,
			Role:           br.Role,
			Rules:          br.Rules,
			ScopeNamespace: br.ScopeNamespace,
		}
		if br.InheritedFromGroup == "" {
			resp.Direct = append(resp.Direct, entry)
		} else {
			inheritedByGroup[br.InheritedFromGroup] = append(inheritedByGroup[br.InheritedFromGroup], entry)
		}
	}
	// Stable group order: match the order EffectiveRules emits implicit
	// groups in (system:authenticated, system:serviceaccounts,
	// system:serviceaccounts:<ns>). Only emit groups with at least one
	// binding so the UI doesn't render empty sections. Only ServiceAccount
	// subjects pick up implicit-group bindings — User/Group subjects have
	// no implicit memberships, so skip the ordering pass for them (calling
	// ImplicitGroupsForSA with the empty namespace would yield a spurious
	// "system:serviceaccounts:" key with a trailing colon).
	if kind == "ServiceAccount" {
		for _, g := range rbac.ImplicitGroupsForSA(namespace) {
			if bs, ok := inheritedByGroup[g]; ok {
				resp.InheritedFromGroups = append(resp.InheritedFromGroups, InheritedGroupBindings{
					GroupName: g,
					Bindings:  bs,
				})
				delete(inheritedByGroup, g)
			}
		}
	}
	// Any remaining groups (e.g. future implicit groups we don't know
	// about) get appended in sorted order so the response shape stays
	// stable across requests - map-iteration order would cause UI flicker.
	remaining := make([]string, 0, len(inheritedByGroup))
	for g := range inheritedByGroup {
		remaining = append(remaining, g)
	}
	sort.Strings(remaining)
	for _, g := range remaining {
		resp.InheritedFromGroups = append(resp.InheritedFromGroups, InheritedGroupBindings{
			GroupName: g,
			Bindings:  inheritedByGroup[g],
		})
	}

	s.writeJSON(w, resp)
}

// handleRBACRole returns the list of bindings that reference a given
// Role or ClusterRole, with subjects inlined.
//
//	GET /api/rbac/role/{kind}/{namespace}/{name}
//
// For ClusterRole, namespace must be passed as "_" by the client (chi
// requires a literal segment).
func (s *Server) handleRBACRole(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	if !s.requireRBACReadable(w, r) {
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" {
		namespace = ""
	}
	if kind != "Role" && kind != "ClusterRole" {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported role kind %q (want Role or ClusterRole)", kind))
		return
	}
	if kind == "Role" && namespace == "" {
		s.writeError(w, http.StatusBadRequest, "Role requires a namespace")
		return
	}
	if kind == "ClusterRole" && namespace != "" {
		s.writeError(w, http.StatusBadRequest, "ClusterRole namespace must be empty or \"_\"")
		return
	}

	idx := s.buildRBACIndex()
	if idx == nil {
		s.writeError(w, http.StatusServiceUnavailable, "RBAC cache not available")
		return
	}

	ref := rbac.RoleRef{Kind: kind, Namespace: namespace, Name: name}
	bindings := idx.BindingsByRole[ref.Key()]

	// To return subjects for each binding we have to look the binding
	// back up in the lister cache. The index only stores BindingRef
	// (kind/ns/name/roleRef), not subjects, because most callers don't
	// need them and we want the index lean.
	cache := k8s.GetResourceCache()
	out := RoleResponse{Role: ref, Bindings: []BindingWithSubjects{}}
	for _, b := range bindings {
		out.Bindings = append(out.Bindings, withSubjects(cache, b))
	}
	s.writeJSON(w, out)
}

// podsUsingServiceAccount returns Pods whose spec.serviceAccountName matches
// the given subject (only meaningful for SA subjects). Empty list for
// User/Group — those don't have a "running as" relationship.
//
// Walks the Pod lister. On clusters with very large pod counts the scan is
// O(N pods) per request; the RBAC index memo (5s TTL) doesn't help here
// because Pods churn faster than RBAC. If this turns into a hot path we can
// build a per-SA inverted index, but for typical SAs (a handful of pods) the
// linear walk is well under a millisecond.
func podsUsingServiceAccount(subj rbac.Subject) []PodRef {
	if subj.Kind != "ServiceAccount" {
		return nil
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	pods := cache.Pods()
	if pods == nil {
		return nil
	}
	all, err := pods.Pods(subj.Namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	out := make([]PodRef, 0)
	for _, p := range all {
		// Empty serviceAccountName defaults to "default" — match the same
		// semantics the Pod renderer uses (`saName = ... || 'default'`)
		// so the SA called "default" shows its full constituency.
		saName := p.Spec.ServiceAccountName
		if saName == "" {
			saName = "default"
		}
		if saName == subj.Name {
			out = append(out, PodRef{Namespace: p.Namespace, Name: p.Name})
		}
	}
	return out
}

func subjectsFromAPI(in []rbacv1.Subject) []rbac.Subject {
	out := make([]rbac.Subject, 0, len(in))
	for _, s := range in {
		out = append(out, rbac.Subject{Kind: s.Kind, Namespace: s.Namespace, Name: s.Name})
	}
	return out
}

// handleRBACNamespace returns the bindings + SA count for a namespace.
//
//	GET /api/rbac/namespace/{namespace}
//
// Same RBAC gating as the subject/role endpoints — requires list
// permissions on rolebindings + clusterrolebindings.
func (s *Server) handleRBACNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	if !s.requireRBACReadable(w, r) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	if namespace == "" {
		s.writeError(w, http.StatusBadRequest, "namespace is required")
		return
	}

	idx := s.buildRBACIndex()
	if idx == nil {
		s.writeError(w, http.StatusServiceUnavailable, "RBAC cache not available")
		return
	}

	nb := idx.BindingsInNamespace(namespace)

	resp := NamespaceRBACResponse{
		Namespace:                           namespace,
		RoleBindings:                        []BindingWithSubjects{},
		ClusterRoleBindingsWithLocalSubject: []BindingWithSubjects{},
	}

	cache := k8s.GetResourceCache()
	for _, b := range nb.RoleBindings {
		resp.RoleBindings = append(resp.RoleBindings, withSubjects(cache, b))
	}
	for _, b := range nb.ClusterRoleBindingsWithLocalSubject {
		resp.ClusterRoleBindingsWithLocalSubject = append(resp.ClusterRoleBindingsWithLocalSubject, withSubjects(cache, b))
	}

	// SA count — quick read from the SA lister; we don't enumerate names
	// here (the UI can fetch the SA list separately if it wants them).
	if cache != nil {
		if l := cache.ServiceAccounts(); l != nil {
			if sas, err := l.ServiceAccounts(namespace).List(labels.Everything()); err == nil {
				resp.ServiceAccountCount = len(sas)
			}
		}
	}
	s.writeJSON(w, resp)
}

// withSubjects looks up the binding's subjects from the cache. Returns the
// binding with an empty Subjects slice on lookup miss; callers should treat
// an empty slice as a soft error rather than panicking.
func withSubjects(cache *k8s.ResourceCache, b rbac.BindingRef) BindingWithSubjects {
	entry := BindingWithSubjects{Binding: b, Subjects: []rbac.Subject{}}
	if cache == nil {
		return entry
	}
	switch b.Kind {
	case "RoleBinding":
		if l := cache.RoleBindings(); l != nil {
			if rb, err := l.RoleBindings(b.Namespace).Get(b.Name); err == nil {
				entry.Subjects = subjectsFromAPI(rb.Subjects)
			}
		}
	case "ClusterRoleBinding":
		if l := cache.ClusterRoleBindings(); l != nil {
			if crb, err := l.Get(b.Name); err == nil {
				entry.Subjects = subjectsFromAPI(crb.Subjects)
			}
		}
	}
	return entry
}

// handleRBACWhoami runs SelfSubjectRulesReview against the apiserver as
// the calling user. Always permitted by Kubernetes for the caller's own
// identity, so no SAR gating needed here.
//
//	GET /api/rbac/whoami?namespace=<ns>
//
// namespace defaults to "default" if not supplied. SSRR is namespace-scoped;
// the rules returned cover only the requested namespace (cluster-scoped
// rules are still included because they apply everywhere).
func (s *Server) handleRBACWhoami(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	client := k8s.ClientFromContext(r.Context())
	if client == nil {
		// Either impersonation failed or the cluster client isn't ready.
		// Both are 503-level — the user can retry once the cluster is up
		// or after fixing impersonation config.
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes client unavailable")
		return
	}

	review := &authv1.SelfSubjectRulesReview{
		Spec: authv1.SelfSubjectRulesReviewSpec{Namespace: ns},
	}
	result, err := client.AuthorizationV1().SelfSubjectRulesReviews().Create(r.Context(), review, metav1.CreateOptions{})
	if err != nil {
		// 403 on SSRR is unusual — it means the apiserver actively denied
		// the review for the caller. Surface as such instead of 500.
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("[rbac] SelfSubjectRulesReview failed for ns=%q: %v", k8s.SanitizeForLog(ns), err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := WhoamiResponse{
		Namespace:        ns,
		ResourceRules:    result.Status.ResourceRules,
		NonResourceRules: result.Status.NonResourceRules,
		Incomplete:       result.Status.Incomplete,
		EvaluationError:  result.Status.EvaluationError,
	}
	s.writeJSON(w, resp)
}
