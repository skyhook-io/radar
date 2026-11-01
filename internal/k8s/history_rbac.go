package k8s

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// RBAC objects hold their configuration at the top level (rules, subjects,
// roleRef), not under spec, and have no status. Each differ compares only
// those fields, so a metadata-only update records nothing.

func diffRBACRole(oldObj, newObj any) ([]FieldChange, []string) {
	oldRules, oldAgg, ok1 := rbacRoleFields(oldObj)
	newRules, newAgg, ok2 := rbacRoleFields(newObj)
	if !ok1 || !ok2 {
		return nil, nil
	}
	var changes []FieldChange
	var summary []string
	if !equalStringSlices(oldRules, newRules) {
		changes = append(changes, FieldChange{Path: "rules", OldValue: oldRules, NewValue: newRules})
		summary = append(summary, "rules changed")
	}
	if !equalStringSlices(oldAgg, newAgg) {
		changes = append(changes, FieldChange{Path: "aggregationRule", OldValue: oldAgg, NewValue: newAgg})
		summary = append(summary, "aggregation rule changed")
	}
	return changes, summary
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

// rbacBindingLifecycleDiff records what a binding grants when it is created
// or deleted; neither event has another object to diff against. A delete
// always records its subjects and roleRef, so a revoked grant can be
// attributed to the subjects it covered. A create records them only when the
// binding grants to a ServiceAccount from outside its namespace: that keeps
// the row out of compact create runs and fixes the subjects it was created
// with, while ordinary creates stay plain.
func rbacBindingLifecycleDiff(kind, namespace, op string, obj any) *DiffInfo {
	if (kind != "RoleBinding" && kind != "ClusterRoleBinding") || (op != "add" && op != "delete") {
		return nil
	}
	if u, ok := obj.(*unstructured.Unstructured); ok {
		obj = diffInputForUnstructured(kind, u)
	}
	subjects, ref, ok := rbacBindingFields(obj)
	if !ok {
		return nil
	}
	if op == "delete" {
		return buildDiff([]FieldChange{
			{Path: "subjects", OldValue: subjects},
			{Path: "roleRef", OldValue: ref},
		}, []string{"deleted; it bound " + ref})
	}
	if len(OutsideServiceAccountSubjects(kind, namespace, subjects)) == 0 {
		return nil
	}
	return buildDiff([]FieldChange{
		{Path: "subjects", NewValue: subjects},
		{Path: "roleRef", NewValue: ref},
	}, []string{"created; it binds " + ref})
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
		return RBACSubjectRefs(b.Subjects), b.RoleRef.Kind + " " + b.RoleRef.Name, true
	case *rbacv1.ClusterRoleBinding:
		return RBACSubjectRefs(b.Subjects), b.RoleRef.Kind + " " + b.RoleRef.Name, true
	}
	return nil, "", false
}

// RBACSubjectRefs renders binding subjects as "ServiceAccount ns/name",
// "User name" or "Group name", sorted. ParseServiceAccountSubjectRef reads
// the ServiceAccount form back.
func RBACSubjectRefs(subjects []rbacv1.Subject) []string {
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		if s.Kind == rbacv1.ServiceAccountKind {
			out = append(out, s.Kind+" "+s.Namespace+"/"+s.Name)
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
