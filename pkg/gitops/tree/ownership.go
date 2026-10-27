package tree

import (
	"context"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

const maxGeneratedResources = 1000
const maxTraversalResources = 10000

// traversalScope describes private cache reads, not what the caller may see.
// The tree already traverses hidden intermediates inside these namespaces;
// canEmit separately gates every returned node and its metadata.
type traversalScope struct{ namespaces []string }

func (s traversalScope) Namespaces() ([]string, bool) { return s.namespaces, s.namespaces == nil }
func (s traversalScope) CanRead(_ resourceid.GroupKind, namespace string) ownership.Decision {
	if namespace == "" || s.namespaces == nil || slices.Contains(s.namespaces, namespace) {
		return ownership.Allowed
	}
	return ownership.Denied
}

func identity(ref ResourceRef) resourceid.Ref {
	return resourceid.NewRef(ref.Group, ref.Kind, ref.Namespace, ref.Name)
}

func (b *Builder) addGenerated(ctx context.Context, view *ownership.View, roots []ResourceRef, nodes map[string]Node, edges map[string]Edge, tool Tool, topoByRef map[string]topology.Node, topoByID map[string]topology.Node, omitted bool) ([]string, bool) {
	index := topology.IndexByResource(b.topo)
	queue := append([]ResourceRef(nil), roots...)
	slices.SortFunc(queue, func(a, b ResourceRef) int { return strings.Compare(identity(a).String(), identity(b).String()) })
	seen := make(map[resourceid.Ref]bool, len(queue))
	queued := make(map[resourceid.Ref]bool, len(queue))
	for _, ref := range queue {
		queued[identity(ref)] = true
	}
	queuedGenerated, generated, incomplete, truncated := 0, 0, false, false
	attach := func(parent ResourceRef, child ownership.Observation) {
		ref := ResourceRef{Group: child.Ref.Group, Kind: child.Ref.Kind, Namespace: child.Ref.Namespace, Name: child.Ref.Name, UID: string(child.UID)}
		if !queued[child.Ref] {
			if queuedGenerated >= maxTraversalResources {
				truncated = true
				return
			}
			queuedGenerated++
			queued[child.Ref] = true
			queue = append(queue, ref)
		}
		if !b.canEmit(ref) {
			omitted = true
			return
		}
		id := nodeID(ref)
		if _, exists := nodes[id]; !exists {
			live, shown := findTopoNode(topoByRef, ref)
			// Topology omits inactive ReplicaSets. Keep that presentation policy
			// while still traversing their current owner references.
			if b.topo != nil && ref.Group == "apps" && ref.Kind == "ReplicaSet" && !shown {
				return
			}
			if generated >= maxGeneratedResources {
				truncated = true
				return
			}
			generated++
			node := syntheticNode(ref, RoleGenerated, tool, "", "", "")
			if shown {
				node = nodeFromTopology(live, ref, RoleGenerated, tool, "", "", "")
			}
			nodes[id] = node
		}
		node := nodes[id]
		node.Dependency = false
		nodes[id] = node
		parentID := nodeID(parent)
		if _, visible := nodes[parentID]; visible && parentID != id {
			edges[edgeKey(parentID, id)] = Edge{Source: parentID, Target: id, Type: EdgeOwns}
		}
	}
	for i := 0; i < len(queue); i++ {
		if ctx.Err() != nil {
			truncated = true
			break
		}
		ref := queue[i]
		key := identity(ref)
		if seen[key] {
			continue
		}
		seen[key] = true
		children := view.Children(key)
		if (ownershipReadFailed(children.Subject) || hasOwnershipReadFailure(children.Gaps)) && b.canEmit(ref) {
			incomplete = true
		}
		owned := make(map[resourceid.Ref]bool, len(children.Children))
		for _, child := range children.Children {
			if ctx.Err() != nil {
				truncated = true
				break
			}
			owned[child.Ref] = true
			attach(ref, child)
		}
		helmClaims := false
		if ownership.IsRecordingManager(key.GroupKind()) {
			members := view.Members(key)
			helmClaims = key.GroupKind() == ownership.FluxHelmRelease && members.Subject.Present() && members.Membership == ownership.MembersUnreadable && len(members.Gaps) == 0
			if (ownershipReadFailed(members.Subject) || hasOwnershipReadFailure(members.Gaps) || members.Subject.Present() && members.Membership == ownership.MembersUnreadable && !helmClaims) && b.canEmit(ref) {
				incomplete = true
			}
			for _, member := range members.Members {
				if ctx.Err() != nil {
					truncated = true
					break
				}
				attach(ref, member)
			}
			// Helm controllers without inventory still identify their resources
			// by labels. Keep that existing projection only for a local manager
			// and a claim on the current object, never a graph edge alone.
			if !helmClaims {
				continue
			}
		}
		live, ok := findTopoNode(topoByRef, ref)
		if !ok {
			continue
		}
		_, outgoing := index.EdgesFor(live.ID)
		for _, edge := range outgoing {
			if ctx.Err() != nil {
				truncated = true
				break
			}
			if edge.Type != topology.EdgeManages {
				continue
			}
			target, ok := topoByID[edge.Target]
			if !ok {
				continue
			}
			targetRef := refFromTopologyNode(target)
			targetKey := identity(targetRef)
			if owned[targetKey] {
				continue
			}
			if helmClaims {
				subject, _ := view.References(targetKey)
				if subject.Present() {
					for _, claim := range subject.Read.Object.Claims {
						if claim.Source == gitops.ClaimFluxHelmRelease && claim.Manager == key {
							attach(ref, subject.Read.Object)
							break
						}
					}
				}
				continue
			}
			sources, missing, unsettled := topology.SpecGenerators(view, targetKey, []topology.ResourceRef{{Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}})
			if (missing || unsettled) && b.canEmit(targetRef) {
				incomplete = true
			}
			if len(sources) != 0 {
				subject, _ := view.References(targetKey)
				if subject.Present() {
					attach(ref, subject.Read.Object)
				}
			}
		}
	}
	var warnings []string
	if incomplete {
		warnings = append(warnings, "Some generated resources could not be checked; the resource tree may be incomplete.")
	}
	if truncated {
		warnings = append(warnings, "Some generated resources could not be included; the resource tree is partial.")
	}
	return warnings, omitted
}

func ownershipReadFailed(subject ownership.Subject) bool {
	return subject.Read.State == ownership.Unavailable && hasOwnershipReadFailure([]ownership.Gap{subject.Read.Gap})
}

// Match the drawer's distinction between collection scope and failed reads.
func hasOwnershipReadFailure(gaps []ownership.Gap) bool {
	return slices.ContainsFunc(gaps, func(g ownership.Gap) bool {
		return g.Kind.Kind != "" && g.Reason != readreason.RadarKindNotCollected && g.Reason != readreason.RadarNamespaceNotCollected
	})
}
