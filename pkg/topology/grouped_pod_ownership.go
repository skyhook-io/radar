package topology

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Grouped Pods keep incarnation evidence without retaining their display nodes.
// The visible group is the authorization boundary for its private members.
type podOwnershipSnapshot struct {
	id, groupID, namespace, name string
	uid                          types.UID
	owners                       []metav1.OwnerReference
}

func (r *RelationshipsIndex) indexGroupedPods(topo *Topology) {
	if len(topo.groupedPods) == 0 {
		return
	}
	r.groupedPods = make(map[string]*podOwnershipSnapshot, len(topo.groupedPods))
	r.groupedPodsByOwner = make(map[string][]string)
	for i := range topo.groupedPods {
		pod := &topo.groupedPods[i]
		if r.nodesByID[pod.groupID] == nil {
			continue
		}
		r.groupedPods[pod.id] = pod
	}
	for i := range topo.groupedPods {
		pod := r.groupedPods[topo.groupedPods[i].id]
		if pod == nil {
			continue
		}
		for _, edge := range r.groupedPodOwnerEdges(pod) {
			r.groupedPodsByOwner[edge.Source] = append(r.groupedPodsByOwner[edge.Source], pod.id)
		}
	}
}

func (r *RelationshipsIndex) nodeForID(id string) *Node {
	if node := r.nodesByID[id]; node != nil {
		return node
	}
	if pod := r.groupedPods[id]; pod != nil {
		return &Node{ID: pod.id, Kind: KindPod, Name: pod.name, uid: pod.uid, ownerReferences: pod.owners, observed: true, Data: map[string]any{"namespace": pod.namespace}}
	}
	return nil
}

func (r *RelationshipsIndex) groupedPodOwnerEdges(pod *podOwnershipSnapshot) []Edge {
	var edges []Edge
	for _, owner := range pod.owners {
		if owner.APIVersion == "" || owner.Kind == "" || owner.Name == "" {
			continue
		}
		parent, matches := r.ResolveObservedOwner(resourceid.OwnerReference(owner.APIVersion, owner.Kind, owner.Name, string(owner.UID), pod.namespace))
		if !matches || parent == nil || !parent.observed && parent.uid == "" || parent.ID == pod.id {
			continue
		}
		controller := owner.Controller != nil && *owner.Controller
		edges = append(edges, Edge{ID: parent.ID + "-to-" + pod.id + "-owner", Source: parent.ID, Target: pod.id, Type: EdgeManages, metadataOwner: true, OwnerController: &controller})
	}
	return edges
}

func (r *RelationshipsIndex) withGroupedPodEdges(id string, incoming, outgoing []Edge) ([]Edge, []Edge) {
	if pod := r.groupedPods[id]; pod != nil {
		incoming = append(append([]Edge(nil), incoming...), r.groupedPodOwnerEdges(pod)...)
	}
	if pods := r.groupedPodsByOwner[id]; len(pods) > 0 {
		outgoing = append([]Edge(nil), outgoing...)
		for _, podID := range pods {
			for _, edge := range r.groupedPodOwnerEdges(r.groupedPods[podID]) {
				if edge.Source == id {
					outgoing = append(outgoing, edge)
				}
			}
		}
	}
	return incoming, outgoing
}
