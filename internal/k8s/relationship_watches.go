package k8s

import (
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func relationshipWatchKinds() []schema.GroupKind {
	typed := map[resourceid.GroupKind]bool{}
	for _, k := range k8score.AllKindListers() {
		typed[resourceid.GroupKind{Group: k.Group(), Kind: k.Kind()}] = true
	}
	var kinds []schema.GroupKind
	for _, k := range relations.Kinds() {
		if !typed[k] {
			kinds = append(kinds, schema.GroupKind{Group: k.Group, Kind: k.Kind})
		}
	}
	return kinds
}
