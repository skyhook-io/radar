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
// Subjects come from the recorded change only: the snapshot a create or
// delete records, or an update's subjects diff. The live binding may have
// changed since.
func bindingGrantReason(e timeline.TimelineEvent) string {
	if (e.Kind != "RoleBinding" && e.Kind != "ClusterRoleBinding") || !eventGroupMatchesTracked(e.Kind, e.APIVersion) {
		return ""
	}
	oldSubjects, newSubjects, role := recordedBinding(e)
	granted := k8s.OutsideServiceAccountSubjects(e.Kind, e.Namespace, subtract(newSubjects, oldSubjects))
	removed := k8s.OutsideServiceAccountSubjects(e.Kind, e.Namespace, subtract(oldSubjects, newSubjects))
	if role == "" {
		// roleRef is immutable, so the cached binding's is the event's while
		// it is the same object.
		role = cachedRoleRef(e)
	}
	if role == "" {
		role = "its role"
	}
	scope := ""
	if e.Kind == "ClusterRoleBinding" {
		scope = " cluster-wide"
	}
	var parts []string
	if len(granted) > 0 {
		parts = append(parts, fmt.Sprintf("grants %s%s to %s", role, scope, namedSubjects(granted)))
	}
	if len(removed) > 0 {
		verb := "no longer grants"
		if e.EventType == timeline.EventTypeDelete {
			verb = "deleted; it granted"
		}
		parts = append(parts, fmt.Sprintf("%s %s%s to %s", verb, role, scope, namedSubjects(removed)))
	}
	return strings.Join(parts, "; ")
}

// recordedBinding reads a binding event's recorded subjects before and after
// the change, and the roleRef a create or delete snapshot carries.
func recordedBinding(e timeline.TimelineEvent) (oldSubjects, newSubjects []string, roleRef string) {
	if e.Diff == nil {
		return nil, nil, ""
	}
	for _, f := range e.Diff.Fields {
		switch f.Path {
		case "subjects":
			oldSubjects, newSubjects = stringList(f.OldValue), stringList(f.NewValue)
		case "roleRef":
			if ref, ok := f.NewValue.(string); ok {
				roleRef = ref
			} else if ref, ok := f.OldValue.(string); ok {
				roleRef = ref
			}
		}
	}
	return oldSubjects, newSubjects, roleRef
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
