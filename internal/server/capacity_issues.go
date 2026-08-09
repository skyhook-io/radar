package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/subject"
)

type capacityIssueProjection struct {
	available bool
	grouped   []issues.Issue
	idsByRef  map[string]map[string]struct{}
	flatByRef map[string][]issues.Issue
}

type capacityIssueMemo struct {
	mu          sync.Mutex
	ttl         time.Duration
	contextName string
	resource    *k8s.ResourceCache
	dynamic     *k8s.DynamicResourceCache
	discovery   *k8s.ResourceDiscovery
	expiresAt   time.Time
	flat        []issues.Issue
}

func newCapacityIssueMemo(ttl time.Duration) *capacityIssueMemo {
	return &capacityIssueMemo{ttl: ttl}
}

func (m *capacityIssueMemo) load(
	contextName string,
	resource *k8s.ResourceCache,
	dynamic *k8s.DynamicResourceCache,
	discovery *k8s.ResourceDiscovery,
	build func() []issues.Issue,
	valid func() bool,
) ([]issues.Issue, bool) {
	if m == nil || m.ttl <= 0 {
		flat := build()
		return flat, valid == nil || valid()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.contextName == contextName && m.resource == resource && m.dynamic == dynamic && m.discovery == discovery && time.Now().Before(m.expiresAt) {
		if valid != nil && !valid() {
			return nil, false
		}
		return m.flat, true
	}
	flat := build()
	if valid != nil && !valid() {
		return nil, false
	}
	m.contextName = contextName
	m.resource = resource
	m.dynamic = dynamic
	m.discovery = discovery
	m.expiresAt = time.Now().Add(m.ttl)
	m.flat = flat
	return flat, true
}

func (m *capacityIssueMemo) clear() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.contextName = ""
	m.resource = nil
	m.dynamic = nil
	m.discovery = nil
	m.expiresAt = time.Time{}
	m.flat = nil
	m.mu.Unlock()
}

func (s *Server) capacityIssuesForRequest(r *http.Request) capacityIssueProjection {
	contextName := k8s.GetContextName()
	provider := issues.NewCacheProvider()
	if provider == nil {
		return newCapacityIssueProjection(nil, false)
	}
	resource, dynamic, discovery := provider.CacheIdentity()
	flat, available := s.capacityIssueMemo.load(contextName, resource, dynamic, discovery, func() []issues.Issue {
		composed := issues.Compose(provider, issues.Filters{
			Limit:                         issues.NoLimit,
			IncludeClusterScopedKarpenter: true,
		})
		capacity := make([]issues.Issue, 0, len(composed))
		for _, issue := range composed {
			if issue.CapacityRelevant || capacityActionCodeForIssue(issue.Reason) != "" {
				capacity = append(capacity, issue)
			}
		}
		return capacity
	}, func() bool {
		return contextName == k8s.GetContextName() &&
			resource == k8s.GetResourceCache() &&
			dynamic == k8s.GetDynamicResourceCache() &&
			discovery == k8s.GetResourceDiscovery()
	})
	if !available {
		return newCapacityIssueProjection(nil, false)
	}
	namespaces := s.capacityNamespacesForUser(r)
	return newCapacityIssueProjection(capacityVisibleIssues(flat, namespaces, s.issueClusterScopedAccess(r), s.issueRelatedResourceAccess(r)), true)
}

func capacityVisibleIssues(
	in []issues.Issue,
	namespaces []string,
	canReadClusterScoped func(kind, group string) bool,
	canReadRelated func(issues.Ref) bool,
) []issues.Issue {
	allowed := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		allowed[namespace] = struct{}{}
	}
	out := make([]issues.Issue, 0, len(in))
	for _, issue := range in {
		if !issues.CanReadIssueRelatedRefs(issue, canReadRelated) {
			continue
		}
		// The memo composes user-agnostically; per-user redaction of
		// referenced-by-NodePool context happens here, at read time.
		issue = issues.RedactIssueRelatedRefs(issue, canReadRelated)
		if issue.Namespace == "" {
			if canReadClusterScoped == nil || canReadClusterScoped(issue.Kind, issue.Group) {
				out = append(out, issue)
			}
			continue
		}
		if namespaces == nil {
			out = append(out, issue)
		} else if _, ok := allowed[issue.Namespace]; ok {
			out = append(out, issue)
		}
	}
	return out
}

func newCapacityIssueProjection(flat []issues.Issue, available bool) capacityIssueProjection {
	projection := capacityIssueProjection{
		available: available,
		grouped:   issues.GroupIssues(flat),
		idsByRef:  map[string]map[string]struct{}{},
		flatByRef: map[string][]issues.Issue{},
	}
	for _, issue := range flat {
		ref := subject.Ref{Group: issue.Group, Kind: issue.Kind, Namespace: issue.Namespace, Name: issue.Name}
		projection.mark(ref, issue.ID)
		if issue.CapacityRelevant {
			key := capacityIssueRefKey(ref)
			projection.flatByRef[key] = append(projection.flatByRef[key], issue)
		}
		if issue.Owner.Kind != "" {
			projection.mark(subject.Ref(issue.Owner), issue.ID)
		}
	}
	return projection
}

func (p capacityIssueProjection) mark(ref subject.Ref, id string) {
	if ref.Kind == "" || ref.Name == "" || id == "" {
		return
	}
	key := capacityIssueRefKey(ref)
	if p.idsByRef[key] == nil {
		p.idsByRef[key] = map[string]struct{}{}
	}
	p.idsByRef[key][id] = struct{}{}
}

func capacityIssueRefKey(ref subject.Ref) string {
	return ref.Group + "\x00" + strings.ToLower(ref.Kind) + "\x00" + ref.Namespace + "\x00" + ref.Name
}

func (p capacityIssueProjection) related(refs ...subject.Ref) []issuesapi.Issue {
	ids := map[string]struct{}{}
	for _, ref := range refs {
		for id := range p.idsByRef[capacityIssueRefKey(ref)] {
			ids[id] = struct{}{}
		}
	}
	out := make([]issuesapi.Issue, 0, len(ids))
	for _, issue := range p.grouped {
		if _, ok := ids[issue.ID]; ok {
			out = append(out, issue)
		}
	}
	return out
}

func (p capacityIssueProjection) attachPools(model *capacitymodel.Model) {
	if model == nil {
		return
	}
	for index := range model.Pools {
		pool := &model.Pools[index]
		refs := []subject.Ref{pool.Observation.Resource.Ref}
		if nodeClass := pool.Observation.NodeClass; nodeClass != nil {
			refs = append(refs, subject.Ref{
				Group: nodeClass.Reference.Group,
				Kind:  nodeClass.Reference.Kind,
				Name:  nodeClass.Reference.Name,
			})
		}
		for _, claim := range pool.Claims {
			refs = append(refs, claim.Resource.Ref)
		}
		pool.Observation.Issues = p.related(refs...)
	}
}

func (p capacityIssueProjection) attachDemand(groups []capacitymodel.DemandGroupModel) {
	for index := range groups {
		group := &groups[index].Group
		podRefs := make(map[string]struct{}, group.PodCount)
		for _, ref := range groups[index].PodRefs() {
			podRefs[capacityIssueRefKey(ref)] = struct{}{}
		}
		flat := make([]issues.Issue, 0)
		for ref := range podRefs {
			for _, issue := range p.flatByRef[ref] {
				dup := issue
				dup.Owner = issues.Ref{}
				if group.Owner != nil {
					dup.Owner = issues.Ref(*group.Owner)
				}
				dup.DiagnosticContext = nil
				dup.IncidentParent = nil
				dup.ChangeContext = nil
				dup.CorrelatedChanges = nil
				dup.NoRecentChanges = nil
				issues.RebindIdentity(&dup)
				flat = append(flat, dup)
			}
		}
		for _, issue := range issues.GroupIssues(flat) {
			if issue.CapacityRelevant {
				group.Issues = append(group.Issues, issue)
			}
		}
	}
}
