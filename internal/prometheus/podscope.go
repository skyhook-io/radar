package prometheus

import (
	"errors"
	"fmt"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

// PodScope is the resolved membership of one workload: the pods it controls
// now, established by controller ownership, and the selection every metrics
// query over it must use.
//
// Membership is the pods running at resolution time. Pods replaced earlier in
// a chart's window are not part of it, so a chart drawn from this scope
// describes the current population over the window rather than every pod that
// served during it.
type PodScope struct {
	Kind      string
	Namespace string
	Name      string

	// CurrentPods are the controlled pods now, sorted, cut to the caller's
	// cap; CurrentTotal is the uncapped count.
	CurrentPods  []string
	CurrentTotal int
	Selection    prom.PodSelection
}

// Partial reports that the pod list was cut by the cap, so the queries cover
// fewer pods than the workload controls.
func (s PodScope) Partial() bool {
	return s.CurrentTotal > len(s.CurrentPods)
}

// ErrPodScopeUnsupportedKind is returned for kinds whose pods cannot be
// established by ownership (a Service, a Node).
var ErrPodScopeUnsupportedKind = errors.New("pod scope: unsupported kind")

// ResolvePodScope establishes membership for kind/namespace/name from the
// cluster cache by controller ownership. It reads local listers only; a
// workload the cache cannot answer for (denied or warming listers, a
// namespace an informer does not cover) is an error, never an empty scope,
// so "no pods" is only ever said when it is true.
func ResolvePodScope(cache *k8s.ResourceCache, kind, namespace, name string, maxPods int) (PodScope, error) {
	scope := PodScope{Kind: kind, Namespace: namespace, Name: name}
	if k := strings.ToLower(strings.TrimSpace(kind)); k == "pod" || k == "pods" {
		// A Pod is its own scope: exactly one name, no ownership to resolve.
		scope.CurrentPods = []string{name}
		scope.CurrentTotal = 1
		scope.Selection = prom.SelectPods(namespace, []string{name})
		return scope, nil
	}
	if k8s.CanonicalWorkloadKind(kind) == "" {
		return PodScope{}, fmt.Errorf("%w: %s", ErrPodScopeUnsupportedKind, kind)
	}

	current, err := k8s.WorkloadPodNames(cache, kind, namespace, name)
	if err != nil {
		return PodScope{}, err
	}
	scope.CurrentTotal = len(current)
	if maxPods > 0 && len(current) > maxPods {
		current = current[:maxPods]
	}
	scope.CurrentPods = current
	scope.Selection = prom.SelectPods(namespace, current)
	return scope, nil
}
