package k8s

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// RBAC objects hold their configuration at the top level (rules, subjects,
// roleRef), not under spec, and have no status. Each differ compares only
// those fields, so a metadata-only update records nothing, except a
// ClusterRole label that decides which aggregated roles include it.

func diffRBACRole(oldObj, newObj any) ([]FieldChange, []string) {
	oldRules, oldAgg, ok1 := rbacRoleFields(oldObj)
	newRules, newAgg, ok2 := rbacRoleFields(newObj)
	if !ok1 || !ok2 {
		return nil, nil
	}
	var changes []FieldChange
	var summary []string
	// Only the rules that changed: an aggregated ClusterRole (admin, edit,
	// view) holds dozens, and is rewritten whenever an aggregated role is
	// installed.
	if removed, added := stringSetDiff(oldRules, newRules), stringSetDiff(newRules, oldRules); len(removed)+len(added) > 0 {
		changes = append(changes, FieldChange{Path: "rules (changed entries)", OldValue: removed, NewValue: added})
		summary = append(summary, fmt.Sprintf("rules changed: %d removed, %d added", len(removed), len(added)))
	}
	if !equalStringSlices(oldAgg, newAgg) {
		changes = append(changes, FieldChange{Path: "aggregationRule", OldValue: oldAgg, NewValue: newAgg})
		summary = append(summary, "aggregation rule changed")
	}
	if label := aggregationLabelChanges(oldObj, newObj); len(label) > 0 {
		changes = append(changes, label...)
		summary = append(summary, "aggregation labels changed")
	}
	return changes, summary
}

// aggregationLabelChanges reports changes to a ClusterRole's labels that
// aggregation reads: rbac.authorization.k8s.io/aggregate-to-* keys, and any
// key a cached ClusterRole's aggregationRule selects on. Such a label edit
// adds or removes this role's rules from every role that aggregates it,
// with no change to this role's own rules.
func aggregationLabelChanges(oldObj, newObj any) []FieldChange {
	oldRole, ok1 := oldObj.(*rbacv1.ClusterRole)
	newRole, ok2 := newObj.(*rbacv1.ClusterRole)
	if !ok1 || !ok2 {
		return nil
	}
	var selected map[string]bool
	var changes []FieldChange
	keys := map[string]bool{}
	for k := range oldRole.Labels {
		keys[k] = true
	}
	for k := range newRole.Labels {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, key := range sorted {
		oldValue, hadOld := oldRole.Labels[key]
		newValue, hasNew := newRole.Labels[key]
		if hadOld == hasNew && oldValue == newValue {
			continue
		}
		if !strings.HasPrefix(key, "rbac.authorization.k8s.io/aggregate-to-") {
			if selected == nil {
				selected = aggregationSelectorKeys()
			}
			if !selected[key] {
				continue
			}
		}
		change := FieldChange{Path: "metadata.labels[" + key + "]"}
		if hadOld {
			change.OldValue = oldValue
		}
		if hasNew {
			change.NewValue = newValue
		}
		changes = append(changes, change)
	}
	return changes
}

// aggregationSelectorKeys returns the label keys the cached ClusterRoles'
// aggregationRule selectors read. Read only when a ClusterRole's labels
// change, which is rare.
func aggregationSelectorKeys() map[string]bool {
	keys := map[string]bool{}
	cache := GetResourceCache()
	if cache == nil || cache.ClusterRoles() == nil {
		return keys
	}
	roles, err := cache.ClusterRoles().List(labels.Everything())
	if err != nil {
		return keys
	}
	for _, role := range roles {
		if role.AggregationRule == nil {
			continue
		}
		for _, selector := range role.AggregationRule.ClusterRoleSelectors {
			for k := range selector.MatchLabels {
				keys[k] = true
			}
			for _, expr := range selector.MatchExpressions {
				keys[expr.Key] = true
			}
		}
	}
	return keys
}

func diffRBACBinding(oldObj, newObj any) ([]FieldChange, []string) {
	oldSubjects, oldRef, ok1 := rbacBindingFields(oldObj)
	newSubjects, newRef, ok2 := rbacBindingFields(newObj)
	if !ok1 || !ok2 {
		return nil, nil
	}
	var changes []FieldChange
	var summary []string
	if !equalStringSlices(oldSubjects, newSubjects) {
		changes = append(changes, FieldChange{Path: "subjects", OldValue: oldSubjects, NewValue: newSubjects})
		summary = append(summary, "subjects changed")
	}
	// roleRef is immutable: it differs only across a delete and recreate,
	// which the recreate join diffs.
	if oldRef != newRef {
		changes = append(changes, FieldChange{Path: "roleRef", OldValue: oldRef, NewValue: newRef})
		summary = append(summary, fmt.Sprintf("roleRef: %s→%s", oldRef, newRef))
	}
	return changes, summary
}

// Paths a recreated binding's diff uses for the subjects and roleRef it kept,
// so the recreate row records the whole grant even when only one side moved.
const (
	UnchangedBindingSubjectsPath = "subjects (unchanged)"
	UnchangedBindingRoleRefPath  = "roleRef (unchanged)"
)

// rbacBindingLifecycleDiff records what a binding grants when it is created
// or deleted, when the event arrives; neither event has another object to
// diff against, and the live object may change or go away later. A create
// records the new side, a delete the old.
func rbacBindingLifecycleDiff(kind, op string, obj any) *DiffInfo {
	if !isRBACBindingKind(kind) || (op != "add" && op != "delete") {
		return nil
	}
	subjects, ref, ok := rbacBindingFields(normalizeRBACObject(kind, obj))
	if !ok {
		return nil
	}
	if op == "delete" {
		return buildDiff([]FieldChange{
			{Path: "subjects", OldValue: subjects},
			{Path: "roleRef", OldValue: ref},
		}, []string{"deleted; it bound " + ref})
	}
	return buildDiff([]FieldChange{
		{Path: "subjects", NewValue: subjects},
		{Path: "roleRef", NewValue: ref},
	}, []string{"created; it binds " + ref})
}

// withRecreatedBindingSnapshot completes a recreated binding's diff with the
// subjects and roleRef the recreate kept, so the row says who holds which
// role without reading the live binding later.
func withRecreatedBindingSnapshot(kind string, newObj any, diff *DiffInfo) *DiffInfo {
	if diff == nil || !isRBACBindingKind(kind) {
		return diff
	}
	subjects, ref, ok := rbacBindingFields(normalizeRBACObject(kind, newObj))
	if !ok {
		return diff
	}
	var hasSubjects, hasRoleRef bool
	for _, f := range diff.Fields {
		hasSubjects = hasSubjects || f.Path == "subjects"
		hasRoleRef = hasRoleRef || f.Path == "roleRef"
	}
	if !hasSubjects {
		diff.Fields = append(diff.Fields, FieldChange{Path: UnchangedBindingSubjectsPath, NewValue: subjects})
	}
	if !hasRoleRef {
		diff.Fields = append(diff.Fields, FieldChange{Path: UnchangedBindingRoleRefPath, NewValue: ref})
	}
	return diff
}

func isRBACBindingKind(kind string) bool {
	return kind == "RoleBinding" || kind == "ClusterRoleBinding"
}

func normalizeRBACObject(kind string, obj any) any {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return diffInputForUnstructured(kind, u)
	}
	return obj
}

// OutsideServiceAccountSubjects keeps the subjects rendered by
// RBACSubjectRefs that are ServiceAccounts acting from outside a binding's
// namespace: any ServiceAccount for a ClusterRoleBinding, one from another
// namespace for a RoleBinding.
func OutsideServiceAccountSubjects(kind, namespace string, refs []string) []string {
	var out []string
	for _, ref := range refs {
		saNamespace, _, ok := ParseServiceAccountSubjectRef(ref)
		if ok && (kind == "ClusterRoleBinding" || saNamespace != namespace) {
			out = append(out, ref)
		}
	}
	return out
}

func rbacRoleFields(obj any) (rules, aggregation []string, ok bool) {
	switch r := obj.(type) {
	case *rbacv1.Role:
		return policyRuleRefs(r.Rules), nil, true
	case *rbacv1.ClusterRole:
		var agg []string
		if r.AggregationRule != nil {
			for i := range r.AggregationRule.ClusterRoleSelectors {
				agg = append(agg, metav1.FormatLabelSelector(&r.AggregationRule.ClusterRoleSelectors[i]))
			}
			sort.Strings(agg)
		}
		return policyRuleRefs(r.Rules), agg, true
	}
	return nil, nil, false
}

func rbacBindingFields(obj any) (subjects []string, roleRef string, ok bool) {
	switch b := obj.(type) {
	case *rbacv1.RoleBinding:
		return RBACSubjectRefs(b.Subjects, b.Namespace), b.RoleRef.Kind + " " + b.RoleRef.Name, true
	case *rbacv1.ClusterRoleBinding:
		return RBACSubjectRefs(b.Subjects, ""), b.RoleRef.Kind + " " + b.RoleRef.Name, true
	}
	return nil, "", false
}

// RBACSubjectRefs renders binding subjects as "ServiceAccount ns/name",
// "User name" or "Group name", sorted. A RoleBinding's ServiceAccount subject
// without a namespace is in the binding's own, bindingNamespace.
// ParseServiceAccountSubjectRef reads the ServiceAccount form back.
func RBACSubjectRefs(subjects []rbacv1.Subject, bindingNamespace string) []string {
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		if s.Kind == rbacv1.ServiceAccountKind {
			namespace := s.Namespace
			if namespace == "" {
				namespace = bindingNamespace
			}
			out = append(out, s.Kind+" "+namespace+"/"+s.Name)
			continue
		}
		out = append(out, s.Kind+" "+s.Name)
	}
	sort.Strings(out)
	return out
}

// ParseServiceAccountSubjectRef returns the namespace and name of a subject
// rendered by RBACSubjectRefs, when it is a ServiceAccount.
func ParseServiceAccountSubjectRef(ref string) (namespace, name string, ok bool) {
	rest, ok := strings.CutPrefix(ref, rbacv1.ServiceAccountKind+" ")
	if !ok {
		return "", "", false
	}
	return strings.Cut(rest, "/")
}

// policyRuleRefs renders each rule as "<groups>/<resources>[names]: <verbs>"
// with every list sorted, so reordering a role's rules or a rule's verbs is
// not a change.
func policyRuleRefs(rules []rbacv1.PolicyRule) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		verbs := sortedJoin(rule.Verbs)
		if len(rule.NonResourceURLs) > 0 {
			out = append(out, sortedJoin(rule.NonResourceURLs)+": "+verbs)
			continue
		}
		groups := make([]string, len(rule.APIGroups))
		for i, g := range rule.APIGroups {
			if g == "" {
				g = "core"
			}
			groups[i] = g
		}
		target := sortedJoin(groups) + "/" + sortedJoin(rule.Resources)
		if len(rule.ResourceNames) > 0 {
			target += "[" + sortedJoin(rule.ResourceNames) + "]"
		}
		out = append(out, target+": "+verbs)
	}
	sort.Strings(out)
	return out
}

func sortedJoin(items []string) string {
	sorted := slices.Clone(items)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// stringSetDiff returns the items of a not in b, in a's order.
func stringSetDiff(a, b []string) []string {
	var out []string
	for _, item := range a {
		if !slices.Contains(b, item) {
			out = append(out, item)
		}
	}
	return out
}
