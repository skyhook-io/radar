package server

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
)

const capacityDemandPageLimit = 25

func (s *Server) handleCapacityDemand(w http.ResponseWriter, r *http.Request) {
	identity := currentCapacityClusterIdentity()
	if !s.requireConnected(w) {
		return
	}
	filters, stateFilter, poolFilter, ownerFilter, podFilter, err := parseCapacityDemandFilters(r.URL.Query())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pageRequest, err := parseCapacityPage(r.URL.Query(), capacityPageOptions{
		Scope:          "demand",
		Filters:        filters,
		ClusterContext: identity.activeClusterContext,
		DefaultLimit:   capacityDemandPageLimit,
		MaxLimit:       capacityDemandPageLimit,
	})
	if err != nil {
		s.writeCapacityPageError(w, err)
		return
	}
	result, ok := s.loadCapacityModel(w, r, identity, false)
	if !ok {
		return
	}
	response := capacityapi.NewDemandResponse(result.meta.GeneratedAt)
	response.ResponseMeta = result.meta
	response.State = result.state
	if result.model == nil || result.snapshot == nil {
		s.writeCapacityResponse(w, result, response)
		return
	}
	if poolFilter != "" {
		if _, found := result.model.Pool(poolFilter); !found {
			s.writeError(w, http.StatusNotFound, "NodePool not found")
			return
		}
	}

	classificationPools, evaluationPools := demandPoolSets(result, poolFilter)
	groups := capacitymodel.BuildDemandGroupModels(capacitymodel.DemandInput{
		GeneratedAt:     result.meta.GeneratedAt,
		Pods:            result.snapshot.Pods,
		ResolvePodOwner: result.snapshot.ResolvePodOwner,
	})
	capacitymodel.ClassifyDemandGroupModels(groups, classificationPools)
	s.capacityIssuesForRequest(r).attachDemand(groups)
	if capacityCoverageObserved(result.meta.Coverage[capacityapi.CoveragePods]) {
		summary := capacitymodel.SummarizeDemandGroupModels(groups)
		response.Summary = &summary
	}
	if stateFilter != "" {
		filtered := groups[:0]
		for _, group := range groups {
			if group.Group.State == stateFilter {
				filtered = append(filtered, group)
			}
		}
		groups = filtered
	}
	if podFilter != nil {
		// The drawer knows the pod; groups key by the pod's TOP owner. Resolve
		// through the same path the grouping uses so the filter can never
		// disagree with it. A pod the caller cannot see — or that stopped
		// pending — matches nothing: the empty result is the honest answer,
		// indistinguishable from any other unmatched subject filter.
		if resolved := demandOwnerForPodFilter(result.snapshot, *podFilter); resolved != nil {
			ownerFilter = resolved
		} else {
			groups = groups[:0]
		}
	}
	if ownerFilter != nil {
		// Server-side subject filter (the Issues deep link): filtering ALL
		// groups here is what makes the link reliable — client-side matching
		// against one 25-group page can miss a live group and read as
		// "already scheduled". An empty result is an honest 200: this owner
		// has no pending demand right now.
		filtered := groups[:0]
		for _, group := range groups {
			if demandGroupMatchesOwner(group, *ownerFilter) {
				filtered = append(filtered, group)
			}
		}
		groups = filtered
	}
	snapshotFingerprint := capacitymodel.DemandSnapshotFingerprint(groups, evaluationPools)
	page, err := paginateCapacityKeysetWithSnapshot(groups, pageRequest, func(group capacitymodel.DemandGroupModel) string {
		return group.Group.ID
	}, snapshotFingerprint)
	if err != nil {
		s.writeCapacityPageError(w, err)
		return
	}
	response.Items = capacitymodel.EvaluateDemandGroupModels(page.items, evaluationPools, 0)
	response.Page = capacityapi.PageInfo{HasMore: page.hasMore, NextCursor: page.nextCursor}
	s.writeCapacityResponse(w, result, response)
}

type demandOwnerFilter struct {
	Namespace string
	Kind      string
	Name      string
}

func demandGroupMatchesOwner(group capacitymodel.DemandGroupModel, owner demandOwnerFilter) bool {
	subject := group.Group.Owner
	return subject != nil &&
		group.Group.Namespace == owner.Namespace &&
		strings.EqualFold(subject.Kind, owner.Kind) &&
		subject.Name == owner.Name
}

type demandPodFilter struct {
	Namespace string
	Name      string
}

func demandOwnerForPodFilter(snapshot *capacitymodel.Snapshot, filter demandPodFilter) *demandOwnerFilter {
	for _, pod := range snapshot.Pods {
		if pod == nil || pod.Namespace != filter.Namespace || pod.Name != filter.Name {
			continue
		}
		owner := capacitymodel.DemandOwnerForPod(pod, snapshot.ResolvePodOwner)
		return &demandOwnerFilter{Namespace: filter.Namespace, Kind: owner.Kind, Name: owner.Name}
	}
	return nil
}

func parseCapacityDemandFilters(query url.Values) (url.Values, capacityapi.DemandState, string, *demandOwnerFilter, *demandPodFilter, error) {
	filters := url.Values{}
	pool := strings.TrimSpace(query.Get("pool"))
	if values := query["pool"]; len(values) > 1 {
		return nil, "", "", nil, nil, fmt.Errorf("pool must be specified at most once")
	} else if pool != "" {
		filters.Set("pool", pool)
	}
	var owner *demandOwnerFilter
	if values := query["owner"]; len(values) > 1 {
		return nil, "", "", nil, nil, fmt.Errorf("owner must be specified at most once")
	} else if raw := strings.TrimSpace(query.Get("owner")); raw != "" {
		parts := strings.Split(raw, "/")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, "", "", nil, nil, fmt.Errorf("owner must be namespace/kind/name")
		}
		owner = &demandOwnerFilter{Namespace: parts[0], Kind: parts[1], Name: parts[2]}
		filters.Set("owner", raw)
	}
	var pod *demandPodFilter
	if values := query["pod"]; len(values) > 1 {
		return nil, "", "", nil, nil, fmt.Errorf("pod must be specified at most once")
	} else if raw := strings.TrimSpace(query.Get("pod")); raw != "" {
		if owner != nil {
			return nil, "", "", nil, nil, fmt.Errorf("specify either pod or owner, not both")
		}
		parts := strings.Split(raw, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, "", "", nil, nil, fmt.Errorf("pod must be namespace/name")
		}
		pod = &demandPodFilter{Namespace: parts[0], Name: parts[1]}
		filters.Set("pod", raw)
	}
	state := capacityapi.DemandState(strings.TrimSpace(query.Get("state")))
	if values := query["state"]; len(values) > 1 {
		return nil, "", "", nil, nil, fmt.Errorf("state must be specified at most once")
	}
	if state != "" {
		valid := map[capacityapi.DemandState]bool{
			capacityapi.DemandWaitingForScheduler: true,
			capacityapi.DemandHeld:                true,
			capacityapi.DemandAwaitingCapacity:    true,
			capacityapi.DemandBlocked:             true,
			capacityapi.DemandUnknown:             true,
		}
		if !valid[state] {
			return nil, "", "", nil, nil, fmt.Errorf("invalid demand state %q", state)
		}
		filters.Set("state", string(state))
	}
	namespaces := parseNamespaces(query)
	if namespaces != nil {
		sort.Strings(namespaces)
		filters["namespaces"] = namespaces
	}
	return filters, state, pool, owner, pod, nil
}

// demandPoolSets derives the two pool views the demand endpoint needs: demand
// state is a property of the whole fleet, so classification always sees every
// pool; the ?pool= filter narrows only which evaluation perspective is
// returned.
func demandPoolSets(result capacityLoadResult, poolFilter string) (classification, evaluation []capacitymodel.DemandPoolInput) {
	classification = capacityDemandPoolInputs(result)
	if poolFilter == "" {
		return classification, classification
	}
	evaluation = make([]capacitymodel.DemandPoolInput, 0, 1)
	for _, input := range classification {
		if input.NodePool.GetName() == poolFilter {
			evaluation = append(evaluation, input)
		}
	}
	return classification, evaluation
}

func capacityDemandPoolInputs(result capacityLoadResult) []capacitymodel.DemandPoolInput {
	shapesByPool := capacitymodel.ObservedMemberShapesByPool(result.snapshot.Nodes, result.snapshot.NodeClaims)
	pools := make([]capacitymodel.DemandPoolInput, 0, len(result.snapshot.NodePools))
	for _, pool := range result.snapshot.NodePools {
		if pool == nil {
			continue
		}
		input := capacitymodel.DemandPoolInput{
			NodePool:             pool,
			ProvisionedKnown:     karpenter.NodePoolStatusResources(pool) != nil,
			ObservedMemberShapes: shapesByPool[pool.GetName()],
		}
		if observed, found := result.model.Pool(pool.GetName()); found && observed.Observation.NodeClass != nil {
			input.NodeClassReady = observed.Observation.NodeClass.Ready
		}
		pools = append(pools, input)
	}
	return pools
}
