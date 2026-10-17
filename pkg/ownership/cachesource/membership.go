package cachesource

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// membersIndex indexes a recording manager by the objects its status lists,
// and by unreadableKey when the list can't be read.
const (
	membersIndex  = "ownership.members"
	unreadableKey = "\x00unreadable"
)

// managerResources are the resources of the RecordingManagers kinds.
var managerResources = map[resourceid.GroupKind]string{
	ownership.ArgoApplication:   "applications",
	ownership.FluxHelmRelease:   "helmreleases",
	ownership.FluxKustomization: "kustomizations",
}

// MembershipIndexers are the indexes Recorders reads on the recording
// managers' informers. A host passes them to its dynamic cache as
// DynamicCacheConfig.Indexers; without them, Recorders reports the manager
// kinds unread.
func MembershipIndexers() map[schema.GroupResource]cache.Indexers {
	out := make(map[schema.GroupResource]cache.Indexers, len(managerResources))
	for gk, resource := range managerResources {
		out[schema.GroupResource{Group: gk.Group, Resource: resource}] = cache.Indexers{membersIndex: managerKeys(gk)}
	}
	return out
}

// reading is one way to read a status entry's namespace. The index can't ask
// discovery a kind's scope — an index function runs when an object changes,
// not when discovery does — so it keys an entry under each reading its kind's
// scope could call for, and a session looks up only the readings the catalog
// does.
type reading byte

const (
	// asRecorded: the namespace the entry records.
	asRecorded reading = 'r'
	// clusterScoped: an entry recording a namespace, read for a
	// cluster-scoped kind, which ignores it.
	clusterScoped reading = 'c'
	// inDestination: an Argo CD entry recording no namespace, read for a
	// namespaced kind, which is then in the Application's destination
	// namespace.
	inDestination reading = 'd'
)

func memberKey(r reading, group, kind, namespace, name string) string {
	return strings.Join([]string{string(r), group, kind, namespace, name}, "\x00")
}

// memberEntry is one object a manager's status lists, as recorded.
type memberEntry struct {
	group, kind, namespace, name string
}

// recordedMembers is what a manager's status says about its members.
type recordedMembers struct {
	state   ownership.MembershipState
	entries []memberEntry
	// destination is an Argo CD Application's destination namespace.
	destination string
}

func membersOf(gk resourceid.GroupKind, u *unstructured.Unstructured) recordedMembers {
	switch gk {
	case ownership.ArgoApplication:
		return argoMembers(u)
	case ownership.FluxHelmRelease, ownership.FluxKustomization:
		return fluxMembers(u)
	}
	return recordedMembers{}
}

// argoMembers reads status.resources. An Application that doesn't deploy to
// this cluster lists objects of another; one with no destination, or whose
// list holds an entry that names no object, has a list Radar can't trust.
func argoMembers(app *unstructured.Unstructured) recordedMembers {
	if !gitops.HasArgoDestination(app) {
		return recordedMembers{}
	}
	if !gitops.IsInClusterDestination(app) {
		return recordedMembers{state: ownership.MembersRemote}
	}
	destination, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "namespace")
	m := recordedMembers{state: ownership.MembersRecorded, destination: destination}
	raw, _, err := unstructured.NestedFieldNoCopy(app.Object, "status", "resources")
	if err != nil {
		return recordedMembers{}
	}
	if raw == nil {
		// Argo CD omits an empty list, so once it has reconciled the
		// Application, no list is an empty one.
		if reconciled, _, _ := unstructured.NestedString(app.Object, "status", "reconciledAt"); reconciled == "" {
			return recordedMembers{}
		}
		return m
	}
	list, ok := raw.([]any)
	if !ok {
		return recordedMembers{}
	}
	for _, item := range list {
		e, ok := item.(map[string]any)
		if !ok {
			return recordedMembers{}
		}
		kind, kindOK := e["kind"].(string)
		name, nameOK := e["name"].(string)
		group, groupOK := optionalString(e["group"])
		namespace, namespaceOK := optionalString(e["namespace"])
		if !kindOK || !nameOK || kind == "" || name == "" || !groupOK || !namespaceOK {
			return recordedMembers{}
		}
		m.entries = append(m.entries, memberEntry{group: group, kind: kind, namespace: namespace, name: name})
	}
	return m
}

// fluxMembers reads a Kustomization's or HelmRelease's status.inventory. One
// applying to another cluster lists that cluster's objects; one without an
// inventory, or with an entry whose ID doesn't parse, has a list Radar can't
// trust.
func fluxMembers(obj *unstructured.Unstructured) recordedMembers {
	if !gitops.FluxTargetsLocalCluster(obj) {
		return recordedMembers{state: ownership.MembersRemote}
	}
	raw, _, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "inventory")
	inventory, isMap := raw.(map[string]any)
	if err != nil || !isMap {
		return recordedMembers{}
	}
	list, isList := inventory["entries"].([]any)
	if !isList && inventory["entries"] != nil {
		return recordedMembers{}
	}
	m := recordedMembers{state: ownership.MembersRecorded}
	for _, item := range list {
		e, ok := item.(map[string]any)
		if !ok {
			return recordedMembers{}
		}
		id, ok := e["id"].(string)
		if !ok {
			return recordedMembers{}
		}
		group, kind, namespace, name, ok := gitops.ParseFluxInventoryID(id)
		if !ok {
			return recordedMembers{}
		}
		m.entries = append(m.entries, memberEntry{group: group, kind: kind, namespace: namespace, name: name})
	}
	return m
}

// optionalString reads a field that is either absent or a string.
func optionalString(v any) (string, bool) {
	if v == nil {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}

// managerKeys is the members index of one manager kind: each entry under
// every reading its kind's scope could call for.
func managerKeys(gk resourceid.GroupKind) cache.IndexFunc {
	return func(obj any) ([]string, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		m := membersOf(gk, u)
		switch m.state {
		case ownership.MembersRemote:
			return nil, nil
		case ownership.MembersUnreadable:
			return []string{unreadableKey}, nil
		}
		keys := make([]string, 0, len(m.entries))
		for _, e := range m.entries {
			keys = append(keys, memberKey(asRecorded, e.group, e.kind, e.namespace, e.name))
			switch {
			case e.namespace != "":
				keys = append(keys, memberKey(clusterScoped, e.group, e.kind, "", e.name))
			case m.destination != "":
				keys = append(keys, memberKey(inDestination, e.group, e.kind, m.destination, e.name))
			}
		}
		return keys, nil
	}
}

func (s *session) Inventory(manager resourceid.Ref) ownership.InventoryRead {
	gk := manager.GroupKind()
	if !ownership.IsRecordingManager(gk) {
		return ownership.InventoryRead{Manager: unavailable(manager, readreason.ReadError)}
	}
	var members recordedMembers
	read := s.readObject(manager, func(u *unstructured.Unstructured) { members = membersOf(gk, u) })
	r := ownership.InventoryRead{Manager: read}
	if read.State != ownership.Present {
		return r
	}
	r.Membership = members.state
	if members.state != ownership.MembersRecorded {
		return r
	}
	r.Members = make([]resourceid.Ref, 0, len(members.entries))
	for _, e := range members.entries {
		r.Members = append(r.Members, s.align(e, members.destination))
	}
	slices.SortFunc(r.Members, compareRefs)
	r.Members = slices.Compact(r.Members)
	return r
}

// align reads an entry as the catalog states its kind's scope.
func (s *session) align(e memberEntry, destination string) resourceid.Ref {
	ref := resourceid.Ref{Group: e.group, Kind: e.kind, Namespace: e.namespace, Name: e.name}
	if k, known := s.catalog.Kind(ref.GroupKind()); known {
		switch {
		case !k.Namespaced:
			ref.Namespace = ""
		case ref.Namespace == "":
			ref.Namespace = destination
		}
	}
	return ref
}

// readings returns the members-index keys under which an entry naming ref is
// filed, as the catalog states ref's kind's scope, or why no entry can name
// ref.
func (s *session) readings(ref resourceid.Ref) ([]string, readreason.Code) {
	if ref.Kind == "" || ref.Name == "" {
		return nil, readreason.ReadError
	}
	gk := ref.GroupKind()
	recorded := memberKey(asRecorded, ref.Group, ref.Kind, ref.Namespace, ref.Name)
	k, known := s.catalog.Kind(gk)
	switch {
	case !known && (s.catalog.Ambiguous(gk) || !s.catalog.Complete()):
		// Radar can't tell the kind's scope.
		return nil, readreason.ReadError
	case !known:
		// No object of the kind can exist; entries name it as recorded.
		return []string{recorded}, ""
	case k.Namespaced && ref.Namespace == "":
		return nil, readreason.NamespaceRequired
	case k.Namespaced:
		return []string{recorded, memberKey(inDestination, ref.Group, ref.Kind, ref.Namespace, ref.Name)}, ""
	case ref.Namespace != "":
		return nil, readreason.ReadError
	}
	return []string{recorded, memberKey(clusterScoped, ref.Group, ref.Kind, "", ref.Name)}, ""
}

func (s *session) Recorders(ref resourceid.Ref) ownership.RecorderRead {
	keys, reason := s.readings(ref)
	if reason != "" {
		return ownership.RecorderRead{Gaps: []ownership.Gap{{Kind: ref.GroupKind(), Namespace: ref.Namespace, Reason: reason}}}
	}
	q := &search{domain: ownership.Everywhere()}
	var recorders []candidate
	var unreadable []resourceid.Ref
	for _, gk := range ownership.RecordingManagers() {
		kind, served := s.catalog.Kind(gk)
		if !served {
			// Not installed, unless discovery missed it.
			if !s.catalog.Complete() || s.catalog.Ambiguous(gk) {
				q.gap(gk, "", readreason.ReadError)
			}
			continue
		}
		if s.caches.Dynamic != nil && !s.caches.Dynamic.Indexes(schema.GroupResource{Group: kind.Group, Resource: kind.Resource}, membersIndex) {
			q.gap(gk, "", readreason.ReadError)
			continue
		}
		for _, key := range keys {
			s.dynamicIndexed(q, kind, membersIndex, key, func(gvr schema.GroupVersionResource, u *unstructured.Unstructured) {
				recorders = append(recorders, candidate{
					ref:        resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: u.GetNamespace(), Name: u.GetName()},
					apiVersion: gvr.GroupVersion().String(),
					obj:        u,
				})
			})
		}
		s.dynamicIndexed(q, kind, membersIndex, unreadableKey, func(_ schema.GroupVersionResource, u *unstructured.Unstructured) {
			unreadable = append(unreadable, resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: u.GetNamespace(), Name: u.GetName()})
		})
	}
	var r ownership.RecorderRead
	slices.SortFunc(recorders, func(a, b candidate) int { return compareRefs(a.ref, b.ref) })
	recorders = slices.CompactFunc(recorders, func(a, b candidate) bool { return a.ref == b.ref })
	for _, c := range recorders {
		if read := observe(c.ref, c.apiVersion, c.obj); read.State == ownership.Present {
			r.Recorders = append(r.Recorders, read.Object)
		} else {
			q.gaps = append(q.gaps, read.Gap)
		}
	}
	slices.SortFunc(unreadable, compareRefs)
	r.Unreadable = slices.Compact(unreadable)
	slices.SortFunc(q.gaps, compareGaps)
	r.Gaps = slices.Compact(q.gaps)
	return r
}
