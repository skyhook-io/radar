package meaningfulchanges

import (
	"fmt"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	rbacv1 "k8s.io/api/rbac/v1"
)

const maxNamedGrantSubjects = 3

// bindingGrantReason names the ServiceAccounts a binding change grants to or
// takes from, when they act from outside the binding's namespace: any
// ServiceAccount for a ClusterRoleBinding, one from another namespace for a
// RoleBinding. Such a grant points at an actor the namespace's own objects
// never mention. Returns "" for other changes.
//
// Subjects come only from the recorded change: the snapshot every create,
// delete and recreate records, or an update's subjects diff. The live
// binding may have changed since. An update records no roleRef; roleRef is
// immutable, so the cached binding's is the event's while it is the same
// object.
func bindingGrantReason(e timeline.TimelineEvent) string {
	if !isRBACBindingKind(e.Kind) || !eventGroupMatchesTracked(e.Kind, e.APIVersion) {
		return ""
	}
	b := recordedBinding(e)
	if b.oldRole == "" && b.newRole == "" {
		b.oldRole = cachedRoleRef(e)
		b.newRole = b.oldRole
	}
	var granted, removed []string
	if b.oldRole != b.newRole && b.oldRole != "" && b.newRole != "" {
		// A recreate that re-pointed roleRef: every subject now holds the new
		// role, and every earlier one lost the old.
		granted, removed = b.newSubjects, b.oldSubjects
	} else {
		granted, removed = subtract(b.newSubjects, b.oldSubjects), subtract(b.oldSubjects, b.newSubjects)
	}
	granted = k8s.OutsideServiceAccountSubjects(e.Kind, e.Namespace, granted)
	removed = k8s.OutsideServiceAccountSubjects(e.Kind, e.Namespace, removed)
	// Say outright that the actor is outside: that is what makes the row a lead.
	scope := " (outside this namespace)"
	if e.Kind == "ClusterRoleBinding" {
		scope = " cluster-wide"
	}
	var parts []string
	if len(granted) > 0 {
		parts = append(parts, grantText("grants", roleOrUnknown(b.newRole), scope, e.Kind, granted))
	}
	if len(removed) > 0 {
		verb := "no longer grants"
		if e.EventType == timeline.EventTypeDelete {
			verb = "deleted; it granted"
		}
		parts = append(parts, grantText(verb, roleOrUnknown(b.oldRole), scope, e.Kind, removed))
	}
	return strings.Join(parts, "; ")
}

func roleOrUnknown(role string) string {
	if role == "" {
		return "its role (not recorded)"
	}
	return role
}

// bindingState is what a binding event recorded before and after it.
type bindingState struct {
	oldSubjects, newSubjects []string
	oldRole, newRole         string
}

// recordedBinding reads a binding event's recorded subjects and roleRef. A
// create snapshot sets only the new side and a delete snapshot only the old;
// a recreate's "(unchanged)" rows set both.
func recordedBinding(e timeline.TimelineEvent) bindingState {
	var b bindingState
	if e.Diff == nil {
		return b
	}
	for _, f := range e.Diff.Fields {
		switch f.Path {
		case "subjects":
			b.oldSubjects, b.newSubjects = stringList(f.OldValue), stringList(f.NewValue)
		case k8s.UnchangedBindingSubjectsPath:
			b.newSubjects = stringList(f.NewValue)
			b.oldSubjects = b.newSubjects
		case "roleRef":
			b.oldRole, _ = f.OldValue.(string)
			b.newRole, _ = f.NewValue.(string)
		case k8s.UnchangedBindingRoleRefPath:
			b.newRole, _ = f.NewValue.(string)
			b.oldRole = b.newRole
		}
	}
	return b
}

// cachedRoleRef returns the roleRef of the cached binding the event is
// about, or "" when it is gone, not cached, or a later object under the same
// name.
func cachedRoleRef(e timeline.TimelineEvent) string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return ""
	}
	var uid string
	var ref rbacv1.RoleRef
	switch e.Kind {
	case "RoleBinding":
		if cache.RoleBindings() == nil {
			return ""
		}
		b, err := cache.RoleBindings().RoleBindings(e.Namespace).Get(e.Name)
		if err != nil {
			return ""
		}
		uid, ref = string(b.UID), b.RoleRef
	case "ClusterRoleBinding":
		if cache.ClusterRoleBindings() == nil {
			return ""
		}
		b, err := cache.ClusterRoleBindings().Get(e.Name)
		if err != nil {
			return ""
		}
		uid, ref = string(b.UID), b.RoleRef
	default:
		return ""
	}
	if e.UID != "" && uid != e.UID {
		return ""
	}
	return ref.Kind + " " + ref.Name
}

// grantText reads "grants Role x cluster-wide to ServiceAccount a/b" for a
// ClusterRoleBinding and "grants Role x to ServiceAccount a/b (outside this
// namespace)" for a RoleBinding.
func grantText(verb, role, scope, kind string, subjects []string) string {
	if kind == "ClusterRoleBinding" {
		return fmt.Sprintf("%s %s%s to %s", verb, role, scope, namedSubjects(subjects))
	}
	return fmt.Sprintf("%s %s to %s%s", verb, role, namedSubjects(subjects), scope)
}

func namedSubjects(refs []string) string {
	if len(refs) <= maxNamedGrantSubjects {
		return strings.Join(refs, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(refs[:maxNamedGrantSubjects], ", "), len(refs)-maxNamedGrantSubjects)
}

func subtract(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

// stringList reads a recorded []string field value, which a persistent store
// returns as []any.
func stringList(value any) []string {
	return overflowPaths(value)
}
