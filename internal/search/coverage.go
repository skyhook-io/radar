package search

import (
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// NamespacedSearchKinds lists the sensitive typed kinds requiring exact caller RBAC.
var NamespacedSearchKinds = []struct{ Kind, Group, Resource string }{
	{"Secret", "", "secrets"},
	{"Role", "rbac.authorization.k8s.io", "roles"},
	{"RoleBinding", "rbac.authorization.k8s.io", "rolebindings"},
}

func (r *Result) addGap(kind, group, reason string) {
	r.Partial = true
	gap := UnsearchedKind{kind, group, reason}
	for _, existing := range r.Unsearched {
		if existing == gap {
			return
		}
	}
	r.Unsearched = append(r.Unsearched, gap)
}

func listErrorReason(err error) string {
	if apierrors.IsForbidden(err) || strings.HasPrefix(err.Error(), "forbidden:") {
		return "sa_forbidden"
	}
	if errors.Is(err, k8s.ErrUnknownKind) {
		return "not_indexed"
	}
	if errors.Is(err, k8s.ErrDynamicNotReady) {
		return "syncing"
	}
	return "list_error"
}

func dynamicObservationReason(observation k8score.DynamicResourceObservation) string {
	switch observation.State {
	case k8score.DynamicObservationSynced:
		return ""
	case k8score.DynamicObservationDenied:
		return "sa_forbidden"
	case k8score.DynamicObservationSyncing:
		if observation.ReasonCode == "sync_stalled" {
			return "sync_failed"
		}
		return "syncing"
	case k8score.DynamicObservationDeferred:
		if observation.ReasonCode == "scope_probe_incomplete" {
			return "syncing"
		}
		return "cold"
	case k8score.DynamicObservationUnsupported:
		return "not_indexed"
	default:
		return "cold"
	}
}
