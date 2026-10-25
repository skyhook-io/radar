package readplan

import (
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type State uint8

const (
	Readable State = iota
	Gap
	OutsideCollection
)

type Extent uint8

const (
	NamespaceSet Extent = iota
	AllNamespaces
	Cluster
	UnknownNamespaces
)

type Verdict struct {
	State State
	Code  readreason.Code
}

type Partition struct {
	Extent     Extent
	Namespaces []string
	Verdict    Verdict
}

type Plan struct {
	Partitions        []Partition
	CollectionLimited bool
	WholeKindReady    bool
}

func (p *Plan) add(extent Extent, namespaces []string, state State, code readreason.Code) {
	if extent == NamespaceSet && len(namespaces) == 0 {
		return
	}
	verdict := Verdict{State: state, Code: code}
	for i := range p.Partitions {
		part := &p.Partitions[i]
		if part.Extent == extent && part.Verdict == verdict {
			part.Namespaces = slices.Compact(slices.Sorted(slices.Values(slices.Concat(part.Namespaces, namespaces))))
			return
		}
	}
	p.Partitions = append(p.Partitions, Partition{Extent: extent, Namespaces: slices.Compact(slices.Sorted(slices.Values(namespaces))), Verdict: verdict})
}

func targetExtent(target readscope.Target, clusterScoped bool) Extent {
	if clusterScoped {
		return Cluster
	}
	if target.Namespaces == nil {
		return AllNamespaces
	}
	return NamespaceSet
}

// Typed assesses cache facts only; callers authorize, read, and project them.
func Typed(cache *k8s.ResourceCache, permissions *k8s.PermissionCheckResult, resource string, target readscope.Target, clusterScoped bool) Plan {
	p := Plan{}
	if !clusterScoped && target.Namespaces != nil && len(target.Namespaces) == 0 {
		return p
	}
	requested := slices.Clone(target.Namespaces)
	slices.Sort(requested)
	requested = slices.Compact(requested)
	a := cache.AssessKindRead(resource, requested)
	p.WholeKindReady = a.Readiness == k8s.KindReady
	extent := targetExtent(target, clusterScoped)
	rejected := permissions.CredentialRejectedNamespaces(resource, target.Namespaces)
	if a.Readiness == k8s.KindUnavailable {
		code := readreason.RadarKindNotCollected
		if permissions != nil {
			if len(rejected) > 0 || apierrors.IsUnauthorized(permissions.ProbeErrors[resource]) {
				code = readreason.RadarCredentialsRejected
			} else if scope, known := permissions.Scopes[resource]; known && !scope.Enabled {
				code = readreason.RadarForbidden
			}
		}
		p.add(extent, target.Namespaces, Gap, code)
		return p
	}
	if clusterScoped || a.AllNamespaces && target.Namespaces == nil {
		switch a.Readiness {
		case k8s.KindReady:
			p.add(extent, target.Namespaces, Readable, "")
		case k8s.KindFailed:
			p.add(extent, target.Namespaces, Gap, readreason.KindSyncTimedOut)
		default:
			p.add(extent, target.Namespaces, Gap, readreason.KindSyncPending)
		}
		return p
	}
	serving := make(map[string]bool, len(a.Read)+len(a.Loading)+len(a.Failed))
	for _, ns := range slices.Concat(a.Read, a.Loading, a.Failed) {
		serving[ns] = true
	}
	rejected = slices.DeleteFunc(rejected, func(ns string) bool { return serving[ns] })
	rejections := make(map[string]bool, len(rejected))
	for _, ns := range rejected {
		rejections[ns] = true
	}
	outside := slices.DeleteFunc(slices.Clone(a.Uncovered), func(ns string) bool { return rejections[ns] })
	p.CollectionLimited = target.Namespaces == nil && !a.AllNamespaces
	p.add(NamespaceSet, a.Read, Readable, "")
	p.add(NamespaceSet, rejected, Gap, readreason.RadarCredentialsRejected)
	p.add(NamespaceSet, outside, OutsideCollection, readreason.RadarNamespaceNotCollected)
	p.add(NamespaceSet, a.Loading, Gap, readreason.KindSyncPending)
	p.add(NamespaceSet, a.Failed, Gap, readreason.KindSyncTimedOut)
	return p
}

type NamespaceObserver interface {
	NamespaceObservation(schema.GroupVersionResource, string) k8score.DynamicResourceObservation
}

// Dynamic never starts watches or probes. On-demand readers replan after I/O.
func Dynamic(cache *k8s.DynamicResourceCache, observations NamespaceObserver, gvr schema.GroupVersionResource, target readscope.Target, clusterScoped bool) Plan {
	p := Plan{}
	if !clusterScoped && target.Namespaces != nil && len(target.Namespaces) == 0 {
		return p
	}
	whole := cache.Observation(gvr)
	extent := targetExtent(target, clusterScoped)
	targets := slices.Clone(target.Namespaces)
	slices.Sort(targets)
	targets = slices.Compact(targets)
	if !clusterScoped && targets == nil && whole.Scope == k8score.DynamicObservationScopeExplicitNamespaces {
		p.CollectionLimited = true
		targets = slices.Compact(slices.Sorted(slices.Values(slices.Concat(whole.Namespaces, whole.UnansweredNamespaces))))
		if len(targets) == 0 {
			state, code := dynamicVerdict(whole, false)
			p.add(UnknownNamespaces, nil, state, code)
			return p
		}
	}
	if clusterScoped || targets == nil {
		state, code := dynamicVerdict(observations.NamespaceObservation(gvr, ""), false)
		if !clusterScoped && whole.Scope != k8score.DynamicObservationScopeCluster && state != Readable {
			extent = UnknownNamespaces
		}
		p.add(extent, nil, state, code)
		return p
	}
	watched := make(map[string]bool, len(whole.Namespaces)+len(whole.UnansweredNamespaces))
	for _, ns := range slices.Concat(whole.Namespaces, whole.UnansweredNamespaces) {
		watched[ns] = true
	}
	groups := map[Verdict][]string{}
	for _, ns := range targets {
		outside := whole.Scope == k8score.DynamicObservationScopeExplicitNamespaces && !watched[ns]
		state, code := dynamicVerdict(observations.NamespaceObservation(gvr, ns), outside)
		v := Verdict{State: state, Code: code}
		groups[v] = append(groups[v], ns)
	}
	for v, namespaces := range groups {
		p.add(NamespaceSet, namespaces, v.State, v.Code)
	}
	slices.SortFunc(p.Partitions, func(a, b Partition) int {
		if a.Verdict.State != b.Verdict.State {
			return int(a.Verdict.State) - int(b.Verdict.State)
		}
		return strings.Compare(string(a.Verdict.Code), string(b.Verdict.Code))
	})
	if target.Namespaces == nil && whole.ReasonCode == "scope_probe_incomplete" && len(whole.UnansweredNamespaces) == 0 {
		p.add(UnknownNamespaces, nil, Gap, readreason.ReadError)
	}
	return p
}

func dynamicVerdict(o k8score.DynamicResourceObservation, outside bool) (State, readreason.Code) {
	switch {
	case o.ReasonCode == "cache_stopped":
		return Gap, readreason.ConnectionChanged
	case o.ReasonCode == "credentials_rejected":
		return Gap, readreason.RadarCredentialsRejected
	case o.ProbeRetrying:
		return Gap, readreason.KindSyncPending
	case len(o.UnansweredNamespaces) > 0 || o.ReasonCode == "scope_probe_incomplete":
		return Gap, readreason.ReadError
	case o.State == k8score.DynamicObservationSynced:
		return Readable, ""
	case o.ReasonCode == "sync_stalled":
		return Gap, readreason.KindSyncTimedOut
	case o.State == k8score.DynamicObservationSyncing:
		return Gap, readreason.KindSyncPending
	case o.State == k8score.DynamicObservationDenied:
		return Gap, readreason.RadarForbidden
	case outside:
		return OutsideCollection, readreason.RadarNamespaceNotCollected
	case o.State == k8score.DynamicObservationUnsupported:
		return Gap, readreason.KindNotWatchable
	case o.ReasonCode == "eager_watch_pending":
		return Gap, readreason.KindSyncPending
	case o.ReasonCode == "scope_probe_failed":
		return Gap, readreason.ReadError
	default:
		return Gap, readreason.RadarKindNotCollected
	}
}
