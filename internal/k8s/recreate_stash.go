package k8s

import (
	"reflect"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Delete+recreate under the same name (kubectl replace --force, delete &&
// apply, Argo Replace=true, immutable-field edits) reaches the timeline as a
// contentless delete + add pair: the old object is gone by the time the add
// arrives, so there is nothing to diff against. The stash keeps the last-seen
// object of recently deleted resources so the next add under the same API
// group, kind, namespace, and name — with a different UID — can be diffed
// against what it replaced.
//
// In-memory only: entries expire after recreateJoinTTL, the store is capped,
// and a cluster-context switch clears it. The timeline itself still records
// both raw events; the join only enriches the add.
const (
	recreateJoinTTL  = 15 * time.Minute
	recreateStashCap = 2048
)

// recreateStashKinds covers every kind the meaningful-changes feed tracks
// (config + desired-state) plus CronJob, whose recreate diff enriches the
// timeline even though the feed doesn't surface it. ReplicaSet-managed Pods
// recreate under generated names, so a same-name join never fires for them;
// StatefulSet Pods would join, but pod churn volume makes stashing every pod
// too costly. Secrets are deliberately excluded — a recreate diff path for
// secret data needs its own redaction review first.
var recreateStashKinds = map[string]bool{
	"ConfigMap":               true,
	"Deployment":              true,
	"StatefulSet":             true,
	"DaemonSet":               true,
	"Rollout":                 true,
	"Service":                 true,
	"Ingress":                 true,
	"NetworkPolicy":           true,
	"HorizontalPodAutoscaler": true,
	"CronJob":                 true,
	"ResourceQuota":           true,
	"LimitRange":              true,
	"Application":             true,
	"Kustomization":           true,
	"HelmRelease":             true,
	"GitRepository":           true,
	"OCIRepository":           true,
	"HelmRepository":          true,
	// Operators frequently delete+recreate webhook configs (cert rotation,
	// re-registration); the join reconstructs the cross-recreate spec diff.
	"MutatingWebhookConfiguration":   true,
	"ValidatingWebhookConfiguration": true,
	"PodDisruptionBudget":            true,
	"Gateway":                        true,
	"GatewayClass":                   true,
	"HTTPRoute":                      true,
	"GRPCRoute":                      true,
	"TCPRoute":                       true,
	"UDPRoute":                       true,
	"TLSRoute":                       true,
	"ReferenceGrant":                 true,
	// A binding's roleRef is immutable, so re-pointing one is a recreate.
	"Role":               true,
	"RoleBinding":        true,
	"ClusterRole":        true,
	"ClusterRoleBinding": true,
}

// RecreateJoinKind reports whether deletes of this kind are stashed for
// recreate-join diffs.
func RecreateJoinKind(kind string) bool { return recreateStashKinds[kind] }

type recreateEntry struct {
	obj       any
	uid       string
	deletedAt time.Time
}

type recreateStash struct {
	mu      sync.Mutex
	entries map[string]recreateEntry
}

func recreateKey(group, kind, namespace, name string) string {
	return resourceid.ResourceKey(group, kind, namespace, name)
}

// stashDeletedForRecreate records a deleted object for a potential
// recreate-join. No-op for kinds outside the allowlist.
func (s *recreateStash) stashDeletedForRecreate(group, kind, namespace, name, uid string, obj any) {
	if obj == nil || !recreateStashKinds[kind] {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]recreateEntry)
	}
	if len(s.entries) >= recreateStashCap {
		s.evictLocked()
	}
	s.entries[recreateKey(group, kind, namespace, name)] = recreateEntry{
		obj:       obj,
		uid:       uid,
		deletedAt: time.Now(),
	}
}

// takeRecreateMatch returns the stashed predecessor of group/kind/ns/name when
// the new UID differs and the delete is within the join TTL. The entry is
// consumed (or discarded, if stale or same-UID) either way.
func (s *recreateStash) takeRecreateMatch(group, kind, namespace, name, newUID string) (any, bool) {
	if !recreateStashKinds[kind] {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := recreateKey(group, kind, namespace, name)
	entry, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	delete(s.entries, key)
	if time.Since(entry.deletedAt) > recreateJoinTTL {
		return nil, false
	}
	// Same UID means the informer replayed an object we already knew, not a
	// recreate — nothing to join.
	if newUID != "" && entry.uid == newUID {
		return nil, false
	}
	return entry.obj, true
}

// evictLocked drops expired entries, then — if the stash is
// still full — the oldest ones, until a quarter of the cap is free. Mass
// namespace teardowns are the stress case; recreates that matter happen
// seconds after the delete, so evicting the oldest first is safe.
func (s *recreateStash) evictLocked() {
	now := time.Now()
	for key, entry := range s.entries {
		if now.Sub(entry.deletedAt) > recreateJoinTTL {
			delete(s.entries, key)
		}
	}
	target := recreateStashCap - recreateStashCap/4
	for len(s.entries) > target {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range s.entries {
			if oldestKey == "" || entry.deletedAt.Before(oldest) {
				oldestKey, oldest = key, entry.deletedAt
			}
		}
		delete(s.entries, oldestKey)
	}
}

func (s *recreateStash) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.entries)
}

// stripStatusForRecreateDiff returns a copy of obj with .status zeroed so the
// recreate diff covers desired state only. Shallow copy: the differs only
// read, never mutate, so sharing spec internals is safe.
func stripStatusForRecreateDiff(obj any) any {
	if obj == nil {
		return nil
	}
	if u, ok := obj.(*unstructured.Unstructured); ok {
		c := u.DeepCopy()
		delete(c.Object, "status")
		return c
	}
	v := reflect.ValueOf(obj)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return obj
	}
	c := reflect.New(v.Elem().Type())
	c.Elem().Set(v.Elem())
	if f := c.Elem().FieldByName("Status"); f.IsValid() && f.CanSet() {
		f.Set(reflect.Zero(f.Type()))
	}
	return c.Interface()
}
