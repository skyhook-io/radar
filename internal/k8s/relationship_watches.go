package k8s

import (
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// typedKinds are the kinds the typed cache holds.
var typedKinds = func() map[resourceid.GroupKind]bool {
	typed := map[resourceid.GroupKind]bool{}
	for _, k := range k8score.AllKindListers() {
		typed[resourceid.GroupKind{Group: k.Group(), Kind: k.Kind()}] = true
	}
	return typed
}()

func isTypedKind(gk resourceid.GroupKind) bool { return typedKinds[gk] }

func relationshipWatchKinds() []schema.GroupKind {
	var kinds []schema.GroupKind
	for _, k := range relations.Kinds() {
		if !isTypedKind(k) {
			kinds = append(kinds, schema.GroupKind{Group: k.Group, Kind: k.Kind})
		}
	}
	return kinds
}
