package prometheus

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

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
	Identities   []prom.WorkloadPodIdentity
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
// cluster cache by controller ownership. It reads local listers, except that
// a Rollout or Workflow root may first wait, bounded by ctx, for its dynamic
// inventory; a workload the cache cannot answer for (denied or warming
// listers, a namespace an informer does not cover) is an error, never an
// empty scope, so "no pods" is only ever said when it is true.
func ResolvePodScope(ctx context.Context, cache *k8s.ResourceCache, kind, namespace, name string, maxPods int) (PodScope, error) {
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

	current, err := k8s.WorkloadPods(ctx, cache, kind, namespace, name)
	if errors.Is(err, k8s.ErrWorkloadNotFound) {
		// A chart of a workload that no longer exists has no current Pods.
		current, err = nil, nil
	}
	if err != nil {
		return PodScope{}, err
	}
	return PodScopeFromPods(kind, namespace, name, current, maxPods), nil
}

// Callers supply name-sorted Pods so caps select a stable population across readers.
func PodScopeFromPods(kind, namespace, name string, pods []*corev1.Pod, maxPods int) PodScope {
	scope := PodScope{Kind: kind, Namespace: namespace, Name: name, CurrentTotal: len(pods)}
	if maxPods > 0 && len(pods) > maxPods {
		pods = pods[:maxPods]
	}
	for _, pod := range pods {
		scope.CurrentPods = append(scope.CurrentPods, pod.Name)
		scope.Identities = append(scope.Identities, prom.WorkloadPodIdentity{Name: pod.Name, UID: string(pod.UID)})
	}
	scope.Selection = prom.SelectPods(namespace, scope.CurrentPods)
	return scope
}
