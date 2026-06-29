package rbac

import (
	"sort"
	"strings"
	"sync"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
)

// Subject identifies a single RBAC principal. Namespace is empty for
// User and Group kinds (those identities are cluster-wide strings, not
// namespaced resources).
type Subject struct {
	Kind      string `json:"kind"` // "ServiceAccount" | "User" | "Group"
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Key returns a stable string suitable for use as a map key. The format
// is `kind/namespace/name` for ServiceAccounts and `kind//name` for
// User/Group (empty namespace segment preserved so the key is unambiguous).
func (s Subject) Key() string {
	return s.Kind + "/" + s.Namespace + "/" + s.Name
}

// RoleRef points at the Role or ClusterRole granted by a binding.
// Namespace is empty when Kind is "ClusterRole".
type RoleRef struct {
	Kind      string `json:"kind"` // "Role" | "ClusterRole"
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Key returns a stable map key. Mirrors Subject.Key shape.
func (r RoleRef) Key() string {
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}

// BindingRef identifies a single RoleBinding or ClusterRoleBinding plus
// the role it points at. Namespace is empty for ClusterRoleBinding.
type BindingRef struct {
	Kind      string  `json:"kind"` // "RoleBinding" | "ClusterRoleBinding"
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	RoleRef   RoleRef `json:"roleRef"`
}

// Index is a snapshot reverse-lookup over the four RBAC kinds. Build it
// once per request (or memoize with a short TTL — see Memoizer) from
// lister output; it makes no assumptions about freshness beyond what the
// caller provides.
type Index struct {
	// BindingsBySubject answers "which bindings reference this subject?"
	// Keyed by Subject.Key(). For ServiceAccount queries, group-inherited
	// bindings (system:authenticated etc.) are NOT included here — see
	// EffectiveRules for the merged view that includes them.
	BindingsBySubject map[string][]BindingRef

	// BindingsByRole answers "who is bound to this role?"
	// Keyed by RoleRef.Key().
	BindingsByRole map[string][]BindingRef

	// Raw role definitions, keyed by RoleRef.Key(), used to resolve rules
	// when computing effective permissions.
	RolesByKey        map[string]*rbacv1.Role
	ClusterRolesByKey map[string]*rbacv1.ClusterRole

	// RoleBindingsByNamespace lists every RoleBinding in a given namespace,
	// regardless of subject count. BindingsBySubject alone misses zero-
	// subject bindings (legal during GitOps reconciliation mid-edit), which
	// would silently truncate the Namespace RBAC view.
	RoleBindingsByNamespace map[string][]BindingRef
}

// BuildIndex computes a fresh Index from typed lister outputs. The inputs
// can be in any order. Nil slices are valid (treated as empty).
//
// Cost is O(B·S) where B is total bindings and S is the average subject
// count per binding — small constants on real clusters. Allocation-heavy
// but designed for memoization.
func BuildIndex(
	roles []*rbacv1.Role,
	clusterRoles []*rbacv1.ClusterRole,
	roleBindings []*rbacv1.RoleBinding,
	clusterRoleBindings []*rbacv1.ClusterRoleBinding,
) *Index {
	idx := &Index{
		BindingsBySubject:       make(map[string][]BindingRef),
		BindingsByRole:          make(map[string][]BindingRef),
		RolesByKey:              make(map[string]*rbacv1.Role, len(roles)),
		ClusterRolesByKey:       make(map[string]*rbacv1.ClusterRole, len(clusterRoles)),
		RoleBindingsByNamespace: make(map[string][]BindingRef),
	}

	for _, r := range roles {
		if r == nil {
			continue
		}
		key := RoleRef{Kind: "Role", Namespace: r.Namespace, Name: r.Name}.Key()
		idx.RolesByKey[key] = r
	}
	for _, cr := range clusterRoles {
		if cr == nil {
			continue
		}
		key := RoleRef{Kind: "ClusterRole", Name: cr.Name}.Key()
		idx.ClusterRolesByKey[key] = cr
	}

	for _, rb := range roleBindings {
		if rb == nil {
			continue
		}
		ref := BindingRef{
			Kind:      "RoleBinding",
			Namespace: rb.Namespace,
			Name:      rb.Name,
			RoleRef:   roleRefFromAPI(rb.RoleRef, rb.Namespace),
		}
		idx.indexBinding(ref, rb.Subjects)
		// Also index by namespace so zero-subject bindings still surface
		// in the per-namespace view (BindingsBySubject misses them).
		if rb.Namespace != "" {
			idx.RoleBindingsByNamespace[rb.Namespace] = append(idx.RoleBindingsByNamespace[rb.Namespace], ref)
		}
	}
	for _, crb := range clusterRoleBindings {
		if crb == nil {
			continue
		}
		ref := BindingRef{
			Kind:    "ClusterRoleBinding",
			Name:    crb.Name,
			RoleRef: roleRefFromAPI(crb.RoleRef, ""),
		}
		idx.indexBinding(ref, crb.Subjects)
	}

	return idx
}

// roleRefFromAPI normalises an rbacv1.RoleRef. bindingNS is the namespace
// of the binding (used only when Kind is "Role"; for ClusterRole the
// namespace is empty regardless).
func roleRefFromAPI(r rbacv1.RoleRef, bindingNS string) RoleRef {
	out := RoleRef{Kind: r.Kind, Name: r.Name}
	if r.Kind == "Role" {
		out.Namespace = bindingNS
	}
	return out
}

func (i *Index) indexBinding(ref BindingRef, subjects []rbacv1.Subject) {
	i.BindingsByRole[ref.RoleRef.Key()] = append(i.BindingsByRole[ref.RoleRef.Key()], ref)
	for _, s := range subjects {
		subj := Subject{Kind: s.Kind, Namespace: s.Namespace, Name: s.Name}
		// Subjects with Kind=User/Group have empty Namespace from the API
		// already; ServiceAccount always carries one. Don't normalise here.
		i.BindingsBySubject[subj.Key()] = append(i.BindingsBySubject[subj.Key()], ref)
	}
}

// BindingRules carries the rules granted by a single binding, plus the
// surrounding context the UI needs to render the grant honestly.
type BindingRules struct {
	Binding BindingRef
	Role    RoleRef
	Rules   []rbacv1.PolicyRule

	// ScopeNamespace is populated when a RoleBinding references a ClusterRole.
	// The rules apply only within this namespace, regardless of what the
	// ClusterRole's rules say in isolation.
	ScopeNamespace string

	// InheritedFromGroup names the implicit group that brought this binding
	// into the subject's effective set (e.g. "system:authenticated"). Empty
	// means the binding directly targets the subject.
	InheritedFromGroup string
}

// EffectiveRules is the merged permission view for a single subject. It
// preserves the per-binding provenance (so the UI can answer "where does
// this permission come from") and also publishes a flattened set for
// quick "does this subject have X" answering.
type EffectiveRules struct {
	Subject     Subject
	ViaBindings []BindingRules
	Flat        []rbacv1.PolicyRule

	// Truncated is true when the result was capped at the soft limit (see
	// MaxFlatRules). The UI should surface this honestly so operators know
	// they're looking at a partial picture.
	Truncated bool
}

// MaxFlatRules caps the flat rule set returned by EffectiveRules to keep
// payloads bounded on large clusters where system:authenticated sweeps in
// hundreds of bindings. The per-binding view is uncapped — callers can
// page through it.
const MaxFlatRules = 500

// EffectiveRules computes the full permission set for a subject. For
// ServiceAccount subjects, implicit group memberships are folded in (see
// ImplicitGroupsForSA for the exact list). Each inherited binding carries
// InheritedFromGroup so the UI can group/collapse them separately.
func (i *Index) EffectiveRules(s Subject) EffectiveRules {
	out := EffectiveRules{Subject: s}

	// Direct bindings — no InheritedFromGroup.
	for _, ref := range i.BindingsBySubject[s.Key()] {
		br := i.expand(ref, "")
		out.ViaBindings = append(out.ViaBindings, br)
	}

	// Implicit group memberships for ServiceAccount subjects. The order
	// here is the order the UI surfaces them in — keep stable.
	if s.Kind == "ServiceAccount" {
		for _, group := range ImplicitGroupsForSA(s.Namespace) {
			groupSubj := Subject{Kind: "Group", Name: group}
			for _, ref := range i.BindingsBySubject[groupSubj.Key()] {
				br := i.expand(ref, group)
				out.ViaBindings = append(out.ViaBindings, br)
			}
		}
	}

	out.Flat, out.Truncated = flatten(out.ViaBindings, MaxFlatRules)
	return out
}

// expand looks up the rules for a binding and packages them as BindingRules.
// inheritedFromGroup is the group name that brought this binding into the
// subject's view, or "" for direct bindings.
func (i *Index) expand(ref BindingRef, inheritedFromGroup string) BindingRules {
	br := BindingRules{Binding: ref, Role: ref.RoleRef, InheritedFromGroup: inheritedFromGroup}
	if ref.Kind == "RoleBinding" && ref.RoleRef.Kind == "ClusterRole" {
		br.ScopeNamespace = ref.Namespace
	}
	switch ref.RoleRef.Kind {
	case "Role":
		if r, ok := i.RolesByKey[ref.RoleRef.Key()]; ok {
			br.Rules = r.Rules
		}
	case "ClusterRole":
		if cr, ok := i.ClusterRolesByKey[ref.RoleRef.Key()]; ok {
			br.Rules = cr.Rules
		}
	}
	// Orphan binding (role lookup miss) yields an empty Rules slice; the
	// BindingRules itself is still returned so the UI can render
	// "references missing role".
	return br
}

// ImplicitGroupsForSA returns the groups every authenticated ServiceAccount
// in the given namespace is implicitly a member of, in display order.
// See https://kubernetes.io/docs/reference/access-authn-authz/rbac/#service-account-permissions
func ImplicitGroupsForSA(namespace string) []string {
	return []string{
		"system:authenticated",
		"system:serviceaccounts",
		"system:serviceaccounts:" + namespace,
	}
}

// flatten dedupes rules across all bindings into a single PolicyRule slice.
// Rules are compared by their structural signature (sorted verbs / groups /
// resources / resourceNames / nonResourceURLs); semantically equivalent
// rules merge. Returns (rules, truncated).
func flatten(via []BindingRules, max int) ([]rbacv1.PolicyRule, bool) {
	seen := make(map[string]struct{})
	out := make([]rbacv1.PolicyRule, 0)
	for _, br := range via {
		for _, r := range br.Rules {
			sig := ruleSignature(r)
			if _, dup := seen[sig]; dup {
				continue
			}
			seen[sig] = struct{}{}
			out = append(out, r)
			if len(out) >= max {
				return out, true
			}
		}
	}
	return out, false
}

func ruleSignature(r rbacv1.PolicyRule) string {
	var b strings.Builder
	writeSorted(&b, r.Verbs)
	b.WriteByte('|')
	writeSorted(&b, r.APIGroups)
	b.WriteByte('|')
	writeSorted(&b, r.Resources)
	b.WriteByte('|')
	writeSorted(&b, r.ResourceNames)
	b.WriteByte('|')
	writeSorted(&b, r.NonResourceURLs)
	return b.String()
}

func writeSorted(b *strings.Builder, in []string) {
	if len(in) == 0 {
		return
	}
	s := append([]string(nil), in...)
	sort.Strings(s)
	b.WriteString(strings.Join(s, ","))
}

// Memoizer caches Index builds for a short TTL. The index is a pure
// projection of lister state; the TTL absorbs request bursts (page load
// fetching SA detail + Pod permissions for the same SA) without
// observable staleness.
type Memoizer struct {
	ttl     time.Duration
	mu      sync.Mutex
	entry   *Index
	builtAt time.Time
}

// NewMemoizer returns a Memoizer with the given TTL. A zero or negative
// TTL disables caching (every Get rebuilds), useful for tests.
func NewMemoizer(ttl time.Duration) *Memoizer {
	return &Memoizer{ttl: ttl}
}

// Get returns a cached Index if a fresh entry exists, otherwise invokes
// build and stores the result. The index is global (not keyed by
// anything) — RBAC is cluster-wide so per-namespace caching offers no
// win. Concurrent cold callers serialize on the mutex; the singleflight
// pattern in pkg/topology/memo.go is overkill here because BuildIndex
// is ~5ms even on large clusters.
func (m *Memoizer) Get(build func() *Index) *Index {
	if m == nil || m.ttl <= 0 {
		return build()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entry != nil && time.Since(m.builtAt) < m.ttl {
		return m.entry
	}
	m.entry = build()
	m.builtAt = time.Now()
	return m.entry
}

// NamespaceBindings is the namespace-scoped slice of the reverse-lookup:
// every binding (RoleBinding or ClusterRoleBinding) that touches the given
// namespace. Used by the Namespace detail view to show "what RBAC is
// configured here" without forcing operators to pivot through individual
// SAs.
type NamespaceBindings struct {
	// Namespaced RoleBindings whose own namespace matches.
	RoleBindings []BindingRef
	// ClusterRoleBindings with at least one ServiceAccount subject in this
	// namespace. We don't include cluster bindings to User/Group subjects
	// because those are cluster-wide identities — they're not "in" a
	// namespace in any meaningful sense.
	ClusterRoleBindingsWithLocalSubject []BindingRef
}

// BindingsInNamespace computes the per-namespace slice from the index.
// ClusterRoleBinding membership is recomputed each call rather than indexed
// at BuildIndex time because (a) the join is cheap (BindingsBySubject walk
// dominated by hash lookups) and (b) indexing it would bloat memory linearly
// with subject count for marginal callers.
func (i *Index) BindingsInNamespace(namespace string) NamespaceBindings {
	out := NamespaceBindings{
		RoleBindings:                        []BindingRef{},
		ClusterRoleBindingsWithLocalSubject: []BindingRef{},
	}
	if namespace == "" {
		return out
	}
	// RoleBindings come straight from RoleBindingsByNamespace — every
	// binding in this namespace, including zero-subject ones (which the
	// per-subject index would miss).
	for _, ref := range i.RoleBindingsByNamespace[namespace] {
		out.RoleBindings = append(out.RoleBindings, ref)
	}
	seenCluster := make(map[string]struct{})
	// ClusterRoleBindings with subjects in this namespace: walk
	// BindingsBySubject for ServiceAccount keys in this namespace.
	for key, refs := range i.BindingsBySubject {
		// Subject key shape: "Kind/Namespace/Name". Cheap prefix match.
		if !startsWith(key, "ServiceAccount/"+namespace+"/") {
			continue
		}
		for _, ref := range refs {
			if ref.Kind != "ClusterRoleBinding" {
				continue
			}
			id := ref.Kind + "/" + ref.Name
			if _, dup := seenCluster[id]; dup {
				continue
			}
			seenCluster[id] = struct{}{}
			out.ClusterRoleBindingsWithLocalSubject = append(out.ClusterRoleBindingsWithLocalSubject, ref)
		}
	}
	return out
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// Invalidate drops the cached entry. Used by the context-switch path:
// after switching to a different cluster, the 5s TTL would briefly
// serve RBAC index data computed against the previous cluster's
// listers, so the caller forces a rebuild on next access.
func (m *Memoizer) Invalidate() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entry = nil
}
