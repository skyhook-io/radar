package search

import (
	"errors"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var NamespacedSearchKinds = []struct{ Kind, Group, Resource string }{
	{"Secret", "", "secrets"},
	{"Role", "rbac.authorization.k8s.io", "roles"},
	{"RoleBinding", "rbac.authorization.k8s.io", "rolebindings"},
	{"ServiceAccount", "", "serviceaccounts"},
	{"NetworkPolicy", "networking.k8s.io", "networkpolicies"},
	{"LimitRange", "", "limitranges"},
	{"ResourceQuota", "", "resourcequotas"},
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
		return "syncing"
	case k8score.DynamicObservationUnsupported:
		return "not_indexed"
	default:
		return "cold"
	}
}
