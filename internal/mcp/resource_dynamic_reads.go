package mcp

import (
	"context"
	"errors"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/summarycontext"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// listDynamicResources lists a kind the dynamic cache serves. namespaces is
// what the caller may read of target (nil: every namespace); gaps carries what
// the caller's own access already withheld. Each namespace is a separate read
// unit: one that fails is a gap, and the list fails only when none succeeds.
func listDynamicResources(ctx context.Context, cache *k8s.ResourceCache, kind, group string, target readTarget, namespaces []string, gaps gapSet, clusterScoped bool, contextMode string) (*mcp.CallToolResult, any, error) {
	dynamicCache := k8s.GetDynamicResourceCache()
	discovery := k8s.GetResourceDiscovery()
	current := func() bool {
		return cache == k8s.GetResourceCache() && dynamicCache == k8s.GetDynamicResourceCache() && discovery == k8s.GetResourceDiscovery()
	}
	if !current() {
		return nil, nil, errConnectionChanged()
	}
	if clusterScoped {
		namespaces = nil
	}
	gvr, found := discoveredGVR(kind, group)
	// No namespaces means an unrestricted caller and no --namespace-scope pin
	// (the pin and explicit requests always arrive as namespaces): the
	// readable scope is whatever the collector covers.
	unrestrictedAll := !clusterScoped && namespaces == nil
	listAt := func(ns string) func() ([]*unstructured.Unstructured, error) {
		return func() ([]*unstructured.Unstructured, error) {
			if !current() {
				return nil, errConnectionChanged()
			}
			if found && dynamicCache != nil {
				if k8s.ShouldBypassDynamicInformer(gvr) {
					return dynamicCache.ListDirect(ctx, gvr, ns)
				}
				return dynamicCache.List(gvr, ns)
			}
			return cache.ListDynamicWithGroup(ctx, kind, ns, group)
		}
	}
	collector := dynamicCollectorTarget(kind, group, "list")

	var rawItems []*unstructured.Unstructured
	listed := false
	if directGVR, ok := dynamicReadGVR(kind, group); unrestrictedAll && ok && k8s.ShouldBypassDynamicInformer(directGVR) {
		// A direct kind has no informer to establish its coverage. When the
		// collector is refused cluster-wide, it reads the namespaces custom
		// resource informers fall back to, which narrows the read.
		items, err := readDynamicScope(ctx, kind, group, "", "list", listAt(""))
		switch {
		case err == nil:
			rawItems, listed = items, true
		case isCollectorForbidden(err) && dynamicCache != nil && len(dynamicCache.FallbackNamespaces()) > 0:
			namespaces = dynamicCache.FallbackNamespaces()
			target = target.narrowedByNamespaceCollection(namespaces, nil)
		default:
			return nil, nil, err
		}
	} else if unrestrictedAll && found && dynamicCache != nil {
		ensureErr := dynamicCache.EnsureWatching(gvr)
		if !current() || dynamicCache.IsStopped() {
			return nil, nil, errConnectionChanged()
		}
		observation := dynamicCache.Observation(gvr)
		facts := readplan.Dynamic(dynamicCache, gvr, nil, false)
		unanswered := make(map[string]bool, len(observation.UnansweredNamespaces))
		for _, ns := range observation.UnansweredNamespaces {
			unanswered[ns] = true
		}
		var intended []string
		for _, part := range facts.Partitions {
			if part.Verdict.State != readplan.OutsideCollection {
				intended = append(intended, part.Namespaces...)
			}
			if part.Verdict.State != readplan.Gap {
				continue
			}
			unresolved := slices.DeleteFunc(slices.Clone(part.Namespaces), func(ns string) bool { return !unanswered[ns] })
			unknown := part.Extent == readplan.UnknownNamespaces && (observation.ProbeRetrying || part.Verdict.Code == readreason.ReadError || part.Verdict.Code == readreason.RadarCredentialsRejected)
			if len(unresolved) > 0 || unknown {
				gaps.add(part.Verdict.Code, dynamicPlanError(ctx, kind, group, "", "list", part.Verdict.Code), unresolved...)
				if unknown {
					if gaps.unknownExtent == nil {
						gaps.unknownExtent = map[readreason.Code]bool{}
					}
					gaps.unknownExtent[part.Verdict.Code] = true
				}
			}
		}
		if observation.Scope == k8score.DynamicObservationScopeExplicitNamespaces {
			namespaces = slices.Clone(observation.Namespaces)
			if namespaces == nil {
				namespaces = []string{}
			}
			target = target.narrowedByNamespaceCollection(intended, nil)
		}
		if ensureErr != nil && (len(namespaces) == 0 || gaps.empty()) {
			if !gaps.empty() {
				return nil, nil, withListCoverage(ctx, gaps.failure(), collector.resource, target, clusterScoped)
			}
			_, err := checkDynamicResourceReadFrom(ctx, kind, group, "", "list", ensureErr, dynamicCache, gvr, found)
			return nil, nil, err
		}
	}

	readAll := false
	var read []string
	switch {
	case listed:
		readAll = true
	case namespaces != nil:
		read = []string{}
		var refused []string
		var refusal error
		uncollected := map[readreason.Code][]string{}
		uncollectedErr := map[readreason.Code]error{}
		for _, ns := range namespaces {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			items, err := readDynamicScope(ctx, kind, group, ns, "list", listAt(ns))
			if err != nil {
				code, classified := readErrorCodeOf(err)
				if !classified || code == readreason.ConnectionChanged {
					return nil, nil, err
				}
				if code == readreason.RadarForbidden {
					refused = append(refused, ns)
					if refusal == nil {
						refusal = collectorCause(err)
					}
				}
				// A namespace Radar doesn't collect or is refused in narrows a
				// read the caller didn't scope; one they named is a gap.
				if (code == readreason.RadarNamespaceNotCollected || code == readreason.RadarForbidden) && !target.explicit && !clusterScoped {
					uncollected[code] = append(uncollected[code], ns)
					if uncollectedErr[code] == nil {
						uncollectedErr[code] = err
					}
					continue
				}
				gaps.add(code, err, ns)
				continue
			}
			read = append(read, ns)
			rawItems = append(rawItems, items...)
		}
		if len(uncollected) > 0 {
			if len(read) > 0 {
				target = target.narrowedByNamespaceCollection(nil, slices.Concat(uncollected[readreason.RadarNamespaceNotCollected], uncollected[readreason.RadarForbidden]))
			} else {
				for code, namespaces := range uncollected {
					gaps.add(code, uncollectedErr[code], namespaces...)
				}
			}
		}
		if len(read) == 0 {
			if len(refused) > 1 {
				// One sentence for every namespace Radar was refused in,
				// rather than the first namespace's.
				var covered []string
				if found && dynamicCache != nil {
					if observation := dynamicCache.Observation(gvr); observation.Scope == k8score.DynamicObservationScopeExplicitNamespaces {
						covered = observation.Namespaces
					}
				}
				gaps.firstErr[readreason.RadarForbidden] = coverageError(ctx, collector, refused, covered, refusal)
			}
			return nil, nil, withListCoverage(ctx, gaps.failure(), collector.resource, target, clusterScoped)
		}
	default:
		items, err := readDynamicScope(ctx, kind, group, "", "list", listAt(""))
		if err != nil {
			if !gaps.empty() && gaps.absorb(err, namespaces) {
				err = gaps.failure()
			}
			return nil, nil, withListCoverage(ctx, err, collector.resource, target, clusterScoped)
		}
		rawItems, readAll = items, true
	}

	allItems := make([]any, 0, len(rawItems))
	for _, item := range rawItems {
		allItems = append(allItems, aicontext.MinifyUnstructured(item, aicontext.LevelSummary))
	}

	if contextMode != "none" {
		// Cluster-scoped CRDs emit issues at namespace="" — passing a
		// namespace-restricted slice would silently zero issueCount on
		// every row. Caller has already gated cluster-scoped reads via
		// canReadClusterScopedKind, so cluster-wide compose is safe.
		idxNamespaces := read
		if readAll || clusterScoped {
			idxNamespaces = nil
		}
		if builder := newResourceSummaryContextBuilder(ctx, idxNamespaces); builder != nil {
			summarycontext.AttachToUnstructuredList(allItems, rawItems, builder)
		}
	}

	if !current() {
		return nil, nil, errConnectionChanged()
	}
	return listResult(ctx, collector.resource, allItems, listCoverageFor(clusterScoped, target, read, readAll, gaps.gaps(ctx, resourceapi.MCPSetLimit, clusterScoped)))
}

// readErrorCodeOf classifies one namespace's failed dynamic read. An API
// server refusal of Radar's own identity is the collector's, not the caller's.
func readErrorCodeOf(err error) (readreason.Code, bool) {
	var re *readError
	if errors.As(err, &re) {
		return re.code, true
	}
	if apierrors.IsForbidden(err) {
		return readreason.RadarForbidden, true
	}
	return "", false
}

func isCollectorForbidden(err error) bool {
	var re *readError
	return errors.As(err, &re) && re.code == readreason.RadarForbidden
}
