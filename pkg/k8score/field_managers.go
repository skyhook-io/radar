package k8score

import (
	"hash/fnv"
	"slices"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FieldManagerWrite names a field manager — the client tool or controller
// that sent a write, as recorded in metadata.managedFields — whose entry
// changed between two observed versions of an object. It names a workflow,
// never a person.
type FieldManagerWrite struct {
	Manager     string `json:"manager"`
	Operation   string `json:"operation,omitempty"`
	Subresource string `json:"subresource,omitempty"`
}

// fieldManagerIndex keeps a compact summary of each cached object's
// managedFields so an update can be attributed after the informer transform
// has stripped them. The summary is captured in the transform, keyed by UID
// and resourceVersion, because client-go may transform a newer version before
// the handler for an older one runs; the update handler compares the summaries
// of the exact old and new versions it was handed.
type fieldManagerIndex struct {
	mu      sync.Mutex
	objects map[fieldManagerUID][]fieldManagerVersion
	// closed is set when the watch stops; a transform still in flight then
	// captures nothing.
	closed bool
}

type fieldManagerUID [16]byte

type fieldManagerVersion struct {
	// resourceVersion is a hash; it only tells apart the few versions of
	// one object held at once.
	resourceVersion uint32
	// replicas is spec.replicas, or -1 when the object has none.
	replicas int32
	entries  []fieldManagerEntry
}

// fieldManagerEntry is one managedFields entry: an interned (manager,
// operation, subresource, apiVersion) and its time in Unix seconds (0 when the
// apiserver set none, as it does for scale-subresource writes).
type fieldManagerEntry struct {
	key  uint32
	time uint32
}

// maxFieldManagerVersions bounds the versions held per object. More than one
// is held only while transforms run ahead of handlers.
const maxFieldManagerVersions = 4

func newFieldManagerIndex() *fieldManagerIndex {
	return &fieldManagerIndex{objects: make(map[fieldManagerUID][]fieldManagerVersion)}
}

func fieldManagerKeyOf(uid string) fieldManagerUID {
	h := fnv.New128a()
	h.Write([]byte(uid))
	var key fieldManagerUID
	h.Sum(key[:0])
	return key
}

func fieldManagerRV(resourceVersion string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(resourceVersion))
	return h.Sum32()
}

// capture records the managedFields summary of obj. It must run before the
// transform strips them.
func (index *fieldManagerIndex) capture(obj any) {
	if index == nil {
		return
	}
	var uid, resourceVersion string
	var entries []fieldManagerEntry
	replicas := int32(-1)
	switch o := obj.(type) {
	case *unstructured.Unstructured:
		uid, resourceVersion = string(o.GetUID()), o.GetResourceVersion()
		entries = unstructuredFieldManagerEntries(o)
		if n, ok, _ := unstructured.NestedInt64(o.Object, "spec", "replicas"); ok {
			replicas = int32(n)
		}
	case metav1.Object:
		uid, resourceVersion = string(o.GetUID()), o.GetResourceVersion()
		entries = typedFieldManagerEntries(o.GetManagedFields())
		replicas = typedReplicas(obj)
	default:
		return
	}
	// Watch-list informers transform objects present at startup twice; the
	// second pass sees the managedFields the first one stripped. An empty
	// summary carries nothing to compare, so it never replaces one.
	if uid == "" || len(entries) == 0 {
		return
	}
	key := fieldManagerKeyOf(uid)
	rv := fieldManagerRV(resourceVersion)
	index.mu.Lock()
	defer index.mu.Unlock()
	if index.closed {
		return
	}
	versions := index.objects[key]
	for _, v := range versions {
		if v.resourceVersion == rv {
			return
		}
	}
	if len(versions) >= maxFieldManagerVersions {
		versions = slices.Delete(versions, 0, 1)
	}
	index.objects[key] = append(versions, fieldManagerVersion{resourceVersion: rv, replicas: replicas, entries: entries})
}

// update returns the managers whose entries changed from oldObj's version to
// newObj's, and drops the summaries older than newObj's. It returns nil when
// either version was not captured or no entry changed.
func (index *fieldManagerIndex) update(oldObj, newObj metav1.Object) []FieldManagerWrite {
	if index == nil || oldObj == nil || newObj == nil {
		return nil
	}
	// A delete and recreate missed in a watch gap arrives as an update
	// across UIDs; the old object's delete never will.
	if oldObj.GetUID() != newObj.GetUID() {
		index.forget(string(oldObj.GetUID()))
		return nil
	}
	key := fieldManagerKeyOf(string(newObj.GetUID()))
	oldRV := fieldManagerRV(oldObj.GetResourceVersion())
	newRV := fieldManagerRV(newObj.GetResourceVersion())

	index.mu.Lock()
	versions := index.objects[key]
	oldAt, newAt := -1, -1
	for i, v := range versions {
		switch v.resourceVersion {
		case oldRV:
			oldAt = i
		case newRV:
			newAt = i
		}
	}
	if newAt < 0 {
		index.mu.Unlock()
		return nil
	}
	next := versions[newAt]
	var prev fieldManagerVersion
	if oldAt >= 0 {
		prev = versions[oldAt]
	}
	if newAt > 0 {
		index.objects[key] = slices.Clone(versions[newAt:])
	}
	index.mu.Unlock()

	if oldAt < 0 {
		return nil
	}
	return changedFieldManagers(prev, next)
}

// close drops every summary and stops further captures.
func (index *fieldManagerIndex) close() {
	if index == nil {
		return
	}
	index.mu.Lock()
	index.closed = true
	clear(index.objects)
	index.mu.Unlock()
}

func (index *fieldManagerIndex) forget(uid string) {
	if index == nil {
		return
	}
	index.mu.Lock()
	delete(index.objects, fieldManagerKeyOf(uid))
	index.mu.Unlock()
}

// changedFieldManagers lists the managers of next whose entry is new or whose
// time advanced. An entry needs a time to count: a new untimed entry, such as
// kubectl-last-applied after a server-side apply, is bookkeeping. Entries that
// disappeared lost all their fields to another manager; they did not write.
//
// The apiserver never sets a time on scale-subresource entries, so their
// writes are inferred instead: a scale entry owns only spec.replicas, and any
// other write to spec.replicas takes it over and removes the entry, so when
// spec.replicas changed and a scale entry remains, its manager wrote it.
func changedFieldManagers(prev, next fieldManagerVersion) []FieldManagerWrite {
	var out []FieldManagerWrite
	add := func(identity FieldManagerWrite) {
		if identity.Manager != "" && !slices.Contains(out, identity) {
			out = append(out, identity)
		}
	}
	replicasChanged := prev.replicas != next.replicas
	for _, entry := range next.entries {
		identity := fieldManagerIdentityOf(entry.key)
		if identity.Subresource == "scale" {
			if replicasChanged {
				add(identity)
			}
			continue
		}
		i := slices.IndexFunc(prev.entries, func(p fieldManagerEntry) bool { return p.key == entry.key })
		if (i < 0 && entry.time > 0) || (i >= 0 && entry.time > prev.entries[i].time) {
			add(identity)
		}
	}
	return out
}

// typedReplicas returns spec.replicas of the typed kinds that have a scale
// subresource, or -1.
func typedReplicas(obj any) int32 {
	var replicas *int32
	switch o := obj.(type) {
	case *appsv1.Deployment:
		replicas = o.Spec.Replicas
	case *appsv1.StatefulSet:
		replicas = o.Spec.Replicas
	case *appsv1.ReplicaSet:
		replicas = o.Spec.Replicas
	}
	if replicas == nil {
		return -1
	}
	return *replicas
}

func typedFieldManagerEntries(fields []metav1.ManagedFieldsEntry) []fieldManagerEntry {
	if len(fields) == 0 {
		return nil
	}
	entries := make([]fieldManagerEntry, 0, len(fields))
	for _, f := range fields {
		var at time.Time
		if f.Time != nil {
			at = f.Time.Time
		}
		entries = append(entries, newFieldManagerEntry(f.Manager, string(f.Operation), f.Subresource, f.APIVersion, at))
	}
	return entries
}

// unstructuredFieldManagerEntries reads managedFields straight from the map:
// GetManagedFields would convert every fieldsV1 tree, which the summary does
// not need.
func unstructuredFieldManagerEntries(u *unstructured.Unstructured) []fieldManagerEntry {
	raw, ok, _ := unstructured.NestedFieldNoCopy(u.Object, "metadata", "managedFields")
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	entries := make([]fieldManagerEntry, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { s, _ := m[k].(string); return s }
		var at time.Time
		if s := str("time"); s != "" {
			at, _ = time.Parse(time.RFC3339, s)
		}
		entries = append(entries, newFieldManagerEntry(str("manager"), str("operation"), str("subresource"), str("apiVersion"), at))
	}
	return entries
}

func newFieldManagerEntry(manager, operation, subresource, apiVersion string, at time.Time) fieldManagerEntry {
	var seconds uint32
	if !at.IsZero() && at.Unix() > 0 {
		seconds = uint32(at.Unix())
	}
	return fieldManagerEntry{
		key:  internFieldManager(fieldManagerIdentity{manager, operation, subresource, apiVersion}),
		time: seconds,
	}
}

type fieldManagerIdentity struct {
	manager, operation, subresource, apiVersion string
}

// fieldManagerNames interns entry identities process-wide. A cluster has a few
// dozen distinct managers; the bound only guards against a client that puts a
// unique value in its manager name. Past it, new identities share one
// unnamed slot, which attribution reports as nothing.
var fieldManagerNames = struct {
	sync.RWMutex
	ids        map[fieldManagerIdentity]uint32
	identities []fieldManagerIdentity
}{
	ids:        map[fieldManagerIdentity]uint32{{}: 0},
	identities: []fieldManagerIdentity{{}},
}

const maxFieldManagerIdentities = 8192

func internFieldManager(identity fieldManagerIdentity) uint32 {
	fieldManagerNames.RLock()
	id, ok := fieldManagerNames.ids[identity]
	fieldManagerNames.RUnlock()
	if ok {
		return id
	}
	fieldManagerNames.Lock()
	defer fieldManagerNames.Unlock()
	if id, ok := fieldManagerNames.ids[identity]; ok {
		return id
	}
	if len(fieldManagerNames.identities) >= maxFieldManagerIdentities {
		return 0
	}
	id = uint32(len(fieldManagerNames.identities))
	fieldManagerNames.identities = append(fieldManagerNames.identities, identity)
	fieldManagerNames.ids[identity] = id
	return id
}

func fieldManagerIdentityOf(id uint32) FieldManagerWrite {
	fieldManagerNames.RLock()
	identity := fieldManagerNames.identities[id]
	fieldManagerNames.RUnlock()
	return FieldManagerWrite{Manager: identity.manager, Operation: identity.operation, Subresource: identity.subresource}
}
