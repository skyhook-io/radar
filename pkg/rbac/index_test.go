package rbac

import (
	"reflect"
	"sort"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- helpers ---------------------------------------------------------------

func role(ns, name string, rules ...rbacv1.PolicyRule) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Rules:      rules,
	}
}

func clusterRole(name string, rules ...rbacv1.PolicyRule) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Rules:      rules,
	}
}

func roleBinding(ns, name string, roleKind, roleName string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		RoleRef:    rbacv1.RoleRef{Kind: roleKind, Name: roleName, APIGroup: "rbac.authorization.k8s.io"},
		Subjects:   subjects,
	}
}

func clusterRoleBinding(name, roleName string, subjects ...rbacv1.Subject) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: roleName, APIGroup: "rbac.authorization.k8s.io"},
		Subjects:   subjects,
	}
}

func saSubject(ns, name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: "ServiceAccount", Namespace: ns, Name: name}
}

func groupSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: "Group", Name: name, APIGroup: "rbac.authorization.k8s.io"}
}

func userSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: "User", Name: name, APIGroup: "rbac.authorization.k8s.io"}
}

func rule(verbs []string, groups []string, resources []string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{Verbs: verbs, APIGroups: groups, Resources: resources}
}

// --- BuildIndex ------------------------------------------------------------

func TestBuildIndex_BindingsBySubject_DirectSA(t *testing.T) {
	idx := BuildIndex(
		[]*rbacv1.Role{role("default", "reader", rule([]string{"get"}, []string{""}, []string{"pods"}))},
		nil,
		[]*rbacv1.RoleBinding{roleBinding("default", "rb", "Role", "reader", saSubject("default", "app"))},
		nil,
	)

	got := idx.BindingsBySubject[Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app"}.Key()]
	if len(got) != 1 {
		t.Fatalf("expected 1 binding for app SA, got %d", len(got))
	}
	if got[0].Name != "rb" || got[0].Kind != "RoleBinding" {
		t.Errorf("wrong binding: %+v", got[0])
	}
	if got[0].RoleRef.Kind != "Role" || got[0].RoleRef.Namespace != "default" || got[0].RoleRef.Name != "reader" {
		t.Errorf("wrong roleRef: %+v", got[0].RoleRef)
	}
}

func TestBuildIndex_BindingsByRole_FoundFromBothBindingKinds(t *testing.T) {
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("view")},
		[]*rbacv1.RoleBinding{roleBinding("ns1", "rb", "ClusterRole", "view", saSubject("ns1", "a"))},
		[]*rbacv1.ClusterRoleBinding{clusterRoleBinding("crb", "view", saSubject("ns2", "b"))},
	)

	got := idx.BindingsByRole[RoleRef{Kind: "ClusterRole", Name: "view"}.Key()]
	if len(got) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(got))
	}
	kinds := []string{got[0].Kind, got[1].Kind}
	sort.Strings(kinds)
	want := []string{"ClusterRoleBinding", "RoleBinding"}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("got kinds %v, want %v", kinds, want)
	}
}

func TestBuildIndex_NilSafe(t *testing.T) {
	// Nil inputs and nil entries within slices must not panic.
	idx := BuildIndex(
		[]*rbacv1.Role{nil, role("ns", "r")},
		nil,
		[]*rbacv1.RoleBinding{nil},
		nil,
	)
	if idx == nil {
		t.Fatal("expected non-nil index")
	}
	if len(idx.RolesByKey) != 1 {
		t.Errorf("expected 1 role registered, got %d", len(idx.RolesByKey))
	}
}

func TestBuildIndex_RoleRefNamespaceFromBinding(t *testing.T) {
	// A RoleBinding's RoleRef.Kind=Role refers to a Role in the binding's
	// namespace. Index must populate Namespace from the binding, not from
	// the (always-empty) rbacv1.RoleRef.
	idx := BuildIndex(
		[]*rbacv1.Role{role("ns1", "reader")},
		nil,
		[]*rbacv1.RoleBinding{roleBinding("ns1", "rb", "Role", "reader", saSubject("ns1", "a"))},
		nil,
	)
	got := idx.BindingsByRole[RoleRef{Kind: "Role", Namespace: "ns1", Name: "reader"}.Key()]
	if len(got) != 1 {
		t.Fatalf("expected 1 binding indexed under ns1/reader, got %d", len(got))
	}
}

// --- EffectiveRules --------------------------------------------------------

func TestEffectiveRules_DirectBindingOnly(t *testing.T) {
	r := rule([]string{"get", "list"}, []string{""}, []string{"pods"})
	idx := BuildIndex(
		[]*rbacv1.Role{role("default", "reader", r)},
		nil,
		[]*rbacv1.RoleBinding{roleBinding("default", "rb", "Role", "reader", saSubject("default", "app"))},
		nil,
	)
	er := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app"})

	if len(er.ViaBindings) != 1 {
		t.Fatalf("expected 1 direct binding, got %d", len(er.ViaBindings))
	}
	if er.ViaBindings[0].InheritedFromGroup != "" {
		t.Errorf("direct binding must not carry InheritedFromGroup: %q", er.ViaBindings[0].InheritedFromGroup)
	}
	if len(er.Flat) != 1 {
		t.Errorf("expected 1 flat rule, got %d", len(er.Flat))
	}
}

func TestEffectiveRules_IncludesImplicitGroupBindings_ForSA(t *testing.T) {
	direct := rule([]string{"get"}, []string{""}, []string{"configmaps"})
	wide := rule([]string{"list"}, []string{""}, []string{"namespaces"})
	idx := BuildIndex(
		[]*rbacv1.Role{role("default", "reader", direct)},
		[]*rbacv1.ClusterRole{clusterRole("view", wide)},
		[]*rbacv1.RoleBinding{roleBinding("default", "rb", "Role", "reader", saSubject("default", "app"))},
		[]*rbacv1.ClusterRoleBinding{clusterRoleBinding("auth-view", "view", groupSubject("system:authenticated"))},
	)
	er := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app"})

	if len(er.ViaBindings) != 2 {
		t.Fatalf("expected 2 bindings (direct + inherited), got %d", len(er.ViaBindings))
	}
	// First is direct, second carries the group attribution.
	if er.ViaBindings[0].InheritedFromGroup != "" {
		t.Errorf("first binding should be direct, got InheritedFromGroup=%q", er.ViaBindings[0].InheritedFromGroup)
	}
	if er.ViaBindings[1].InheritedFromGroup != "system:authenticated" {
		t.Errorf("second binding should be inherited via system:authenticated, got %q", er.ViaBindings[1].InheritedFromGroup)
	}
	if len(er.Flat) != 2 {
		t.Errorf("expected 2 flat rules, got %d", len(er.Flat))
	}
}

func TestEffectiveRules_NamespaceSpecificImplicitGroup(t *testing.T) {
	// A binding to "system:serviceaccounts:default" should be included for
	// SAs in default but not for SAs in other namespaces.
	r := rule([]string{"get"}, []string{""}, []string{"secrets"})
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("secret-reader", r)},
		nil,
		[]*rbacv1.ClusterRoleBinding{clusterRoleBinding(
			"ns-grant", "secret-reader",
			groupSubject("system:serviceaccounts:default"),
		)},
	)

	defaultSA := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app"})
	if len(defaultSA.ViaBindings) != 1 {
		t.Errorf("SA in default ns should inherit the binding, got %d", len(defaultSA.ViaBindings))
	}

	otherSA := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "kube-system", Name: "x"})
	if len(otherSA.ViaBindings) != 0 {
		t.Errorf("SA in kube-system must NOT inherit a default-scoped grant, got %d bindings", len(otherSA.ViaBindings))
	}
}

func TestEffectiveRules_ImplicitGroupsSkippedForNonSA(t *testing.T) {
	// User/Group subjects should NOT pick up system:authenticated grants
	// automatically — those are SA-specific implicit memberships. (A real
	// User may *also* be in system:authenticated, but mapping User identity
	// to group memberships requires the auth layer to tell us — out of scope.)
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("view", rule([]string{"get"}, []string{""}, []string{"pods"}))},
		nil,
		[]*rbacv1.ClusterRoleBinding{clusterRoleBinding("auth-view", "view", groupSubject("system:authenticated"))},
	)
	er := idx.EffectiveRules(Subject{Kind: "User", Name: "alice@example.com"})
	if len(er.ViaBindings) != 0 {
		t.Errorf("User subject must not auto-include system:authenticated bindings, got %d", len(er.ViaBindings))
	}
}

func TestEffectiveRules_OrphanBinding_EmptyRules(t *testing.T) {
	// Binding references a Role that doesn't exist — index returns the
	// binding with empty Rules, never panics.
	idx := BuildIndex(
		nil,
		nil,
		[]*rbacv1.RoleBinding{roleBinding("default", "orphan", "Role", "missing", saSubject("default", "app"))},
		nil,
	)
	er := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app"})
	if len(er.ViaBindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(er.ViaBindings))
	}
	if len(er.ViaBindings[0].Rules) != 0 {
		t.Errorf("orphan binding should yield empty rules, got %d", len(er.ViaBindings[0].Rules))
	}
	if len(er.Flat) != 0 {
		t.Errorf("flat rules should be empty for orphan binding, got %d", len(er.Flat))
	}
}

func TestEffectiveRules_ScopeNamespace_RBtoClusterRole(t *testing.T) {
	r := rule([]string{"get"}, []string{""}, []string{"secrets"})
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("secret-reader", r)},
		[]*rbacv1.RoleBinding{roleBinding("ns1", "rb", "ClusterRole", "secret-reader", saSubject("ns1", "app"))},
		nil,
	)
	er := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "ns1", Name: "app"})
	if len(er.ViaBindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(er.ViaBindings))
	}
	if er.ViaBindings[0].ScopeNamespace != "ns1" {
		t.Errorf("expected ScopeNamespace=ns1 for RB→ClusterRole, got %q", er.ViaBindings[0].ScopeNamespace)
	}
}

func TestEffectiveRules_ScopeNamespace_ClusterRoleBinding_Empty(t *testing.T) {
	r := rule([]string{"get"}, []string{""}, []string{"secrets"})
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("secret-reader", r)},
		nil,
		[]*rbacv1.ClusterRoleBinding{clusterRoleBinding("crb", "secret-reader", saSubject("default", "app"))},
	)
	er := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app"})
	if len(er.ViaBindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(er.ViaBindings))
	}
	if er.ViaBindings[0].ScopeNamespace != "" {
		t.Errorf("ClusterRoleBinding must not set ScopeNamespace, got %q", er.ViaBindings[0].ScopeNamespace)
	}
}

func TestEffectiveRules_AggregatedClusterRole_UsesPopulatedRules(t *testing.T) {
	// Aggregated ClusterRoles have rules[] populated by the controller; we
	// just read them. The aggregationRule itself is not evaluated client-side.
	aggregated := clusterRole("aggregate-to-view",
		rule([]string{"get"}, []string{""}, []string{"pods"}),
		rule([]string{"list"}, []string{""}, []string{"pods"}),
	)
	aggregated.AggregationRule = &rbacv1.AggregationRule{}
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{aggregated},
		nil,
		[]*rbacv1.ClusterRoleBinding{clusterRoleBinding("crb", "aggregate-to-view", saSubject("default", "app"))},
	)
	er := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app"})
	if len(er.Flat) != 2 {
		t.Errorf("expected 2 flat rules from aggregated CR, got %d", len(er.Flat))
	}
}

func TestEffectiveRules_FlatDedupes(t *testing.T) {
	// Two bindings granting the same rule should flatten to one entry.
	r := rule([]string{"get"}, []string{""}, []string{"pods"})
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("a", r), clusterRole("b", r)},
		nil,
		[]*rbacv1.ClusterRoleBinding{
			clusterRoleBinding("crb-a", "a", saSubject("default", "app")),
			clusterRoleBinding("crb-b", "b", saSubject("default", "app")),
		},
	)
	er := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app"})
	if len(er.ViaBindings) != 2 {
		t.Errorf("ViaBindings should preserve both grants for provenance, got %d", len(er.ViaBindings))
	}
	if len(er.Flat) != 1 {
		t.Errorf("Flat should dedupe identical rules to 1, got %d", len(er.Flat))
	}
}

func TestEffectiveRules_FlatTruncation(t *testing.T) {
	// Build a CR with MaxFlatRules+10 distinct rules; truncated flag should fire.
	rules := make([]rbacv1.PolicyRule, 0, MaxFlatRules+10)
	for i := 0; i < MaxFlatRules+10; i++ {
		rules = append(rules, rule([]string{"get"}, []string{""}, []string{"resource" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))}))
	}
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("huge", rules...)},
		nil,
		[]*rbacv1.ClusterRoleBinding{clusterRoleBinding("crb", "huge", saSubject("default", "app"))},
	)
	er := idx.EffectiveRules(Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app"})
	if !er.Truncated {
		t.Error("expected Truncated=true when flat rules exceed cap")
	}
	if len(er.Flat) != MaxFlatRules {
		t.Errorf("flat rules should be capped at %d, got %d", MaxFlatRules, len(er.Flat))
	}
}

func TestEffectiveRules_UserSubject_DirectOnly(t *testing.T) {
	// Users get direct bindings only (no implicit groups). Confirm we don't
	// regress and start trying to use namespace="" in implicit lookups.
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("view", rule([]string{"get"}, []string{""}, []string{"pods"}))},
		nil,
		[]*rbacv1.ClusterRoleBinding{
			clusterRoleBinding("crb", "view", userSubject("alice@example.com")),
		},
	)
	er := idx.EffectiveRules(Subject{Kind: "User", Name: "alice@example.com"})
	if len(er.ViaBindings) != 1 {
		t.Fatalf("expected 1 direct binding, got %d", len(er.ViaBindings))
	}
}

// --- ruleSignature ---------------------------------------------------------

func TestRuleSignature_OrderIndependent(t *testing.T) {
	a := rule([]string{"get", "list"}, []string{""}, []string{"pods", "services"})
	b := rule([]string{"list", "get"}, []string{""}, []string{"services", "pods"})
	if ruleSignature(a) != ruleSignature(b) {
		t.Error("rule signature must be order-independent across verbs/resources")
	}
}

func TestRuleSignature_DistinguishesFields(t *testing.T) {
	get := rule([]string{"get"}, []string{""}, []string{"pods"})
	list := rule([]string{"list"}, []string{""}, []string{"pods"})
	if ruleSignature(get) == ruleSignature(list) {
		t.Error("get and list on same resource must produce distinct signatures")
	}
}

// --- BindingsInNamespace ---------------------------------------------------

func TestBindingsInNamespace_NamespacedRoleBindings(t *testing.T) {
	idx := BuildIndex(
		nil, nil,
		[]*rbacv1.RoleBinding{
			roleBinding("ns1", "rb-1", "Role", "r", saSubject("ns1", "sa1")),
			roleBinding("ns2", "rb-2", "Role", "r", saSubject("ns2", "sa2")),
		},
		nil,
	)
	got := idx.BindingsInNamespace("ns1")
	if len(got.RoleBindings) != 1 || got.RoleBindings[0].Name != "rb-1" {
		t.Errorf("expected rb-1 only, got %+v", got.RoleBindings)
	}
	if len(got.ClusterRoleBindingsWithLocalSubject) != 0 {
		t.Errorf("expected no cluster bindings, got %d", len(got.ClusterRoleBindingsWithLocalSubject))
	}
}

func TestBindingsInNamespace_ClusterBindingsTouchingNamespace(t *testing.T) {
	// A ClusterRoleBinding referencing an SA in ns1 should appear in ns1's
	// view, but not in ns2's (unless ns2 has its own subject in the binding).
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("admin")},
		nil,
		[]*rbacv1.ClusterRoleBinding{
			clusterRoleBinding("crb-1", "admin",
				saSubject("ns1", "sa1"),
				saSubject("ns2", "sa2"),
			),
		},
	)
	gotNs1 := idx.BindingsInNamespace("ns1")
	gotNs2 := idx.BindingsInNamespace("ns2")
	if len(gotNs1.ClusterRoleBindingsWithLocalSubject) != 1 {
		t.Errorf("ns1 should see crb-1, got %d", len(gotNs1.ClusterRoleBindingsWithLocalSubject))
	}
	if len(gotNs2.ClusterRoleBindingsWithLocalSubject) != 1 {
		t.Errorf("ns2 should also see crb-1 (its SA is also a subject), got %d", len(gotNs2.ClusterRoleBindingsWithLocalSubject))
	}
}

func TestBindingsInNamespace_ClusterBindingsWithOnlyGroupSubjects_Excluded(t *testing.T) {
	// A ClusterRoleBinding targeting system:authenticated has no
	// namespace-local subject — including it in every namespace's view
	// would surface 100% of system bindings as "touching ns1" which is
	// useless noise.
	idx := BuildIndex(
		nil,
		[]*rbacv1.ClusterRole{clusterRole("view")},
		nil,
		[]*rbacv1.ClusterRoleBinding{
			clusterRoleBinding("auth-view", "view", groupSubject("system:authenticated")),
		},
	)
	got := idx.BindingsInNamespace("ns1")
	if len(got.ClusterRoleBindingsWithLocalSubject) != 0 {
		t.Errorf("group-only cluster bindings must NOT count as namespace-local, got %d", len(got.ClusterRoleBindingsWithLocalSubject))
	}
}

func TestBindingsInNamespace_EmptyNamespace(t *testing.T) {
	idx := BuildIndex(nil, nil, []*rbacv1.RoleBinding{
		roleBinding("ns1", "rb-1", "Role", "r", saSubject("ns1", "sa1")),
	}, nil)
	got := idx.BindingsInNamespace("")
	if len(got.RoleBindings) != 0 {
		t.Errorf("empty namespace must return empty slice, got %d", len(got.RoleBindings))
	}
}

// --- Memoizer --------------------------------------------------------------

func TestMemoizer_ReusesCachedIndex(t *testing.T) {
	m := NewMemoizer(time.Second)
	calls := 0
	build := func() *Index {
		calls++
		return BuildIndex(nil, nil, nil, nil)
	}
	a := m.Get(build)
	b := m.Get(build)
	if calls != 1 {
		t.Errorf("expected 1 build call, got %d", calls)
	}
	if a != b {
		t.Error("expected cached entry to be returned by pointer-equality")
	}
}

func TestMemoizer_ZeroTTLDisablesCaching(t *testing.T) {
	m := NewMemoizer(0)
	calls := 0
	build := func() *Index {
		calls++
		return BuildIndex(nil, nil, nil, nil)
	}
	m.Get(build)
	m.Get(build)
	if calls != 2 {
		t.Errorf("expected 2 build calls with TTL=0, got %d", calls)
	}
}

