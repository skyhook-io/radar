package search

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// ScannedObject is one object a Scan reached. Object is borrowed from the
// cache: read it, don't change or retain it. A kind served both as a typed and
// as a watched dynamic resource is visited once per path.
type ScannedObject struct {
	Kind   string
	Group  string
	Object metav1.Object
}

// ScanAccess is the caller's access for a Scan, with the meanings of the
// Options fields of the same names. Build it as for a Search.
type ScanAccess struct {
	Selection            readscope.Selection
	NamespaceCheckFailed bool
	TypedKindAccess      func(namespaces []string, group, resource string) (allowed, denied, unchecked []string)
	CanReadClusterScoped func(kind, group, resource string) (allowed, authoritative bool)
	DynamicKindAccess    *DynamicKindAccess
}

// Scan walks what a broad Search walks, under the same per-user gates and with
// the same coverage, and passes every object in scope to visit instead of
// scoring it, so it has no result limit. Kinds that skip selects are never
// listed, whatever the caller may read. Like a broad Search, it reads the kinds
// Radar already watches and warms none. Before publishing, the caller must
// verify that its captured provider is still current.
func Scan(ctx context.Context, p Provider, access ScanAccess, skip func(kind, group string) bool, visit func(ScannedObject)) (resourceapi.SearchCoverage, error) {
	res, err := Search(ctx, p, Query{}, Options{
		Selection:            access.Selection,
		NamespaceCheckFailed: access.NamespaceCheckFailed,
		TypedKindAccess:      access.TypedKindAccess,
		CanReadClusterScoped: access.CanReadClusterScoped,
		DynamicKindAccess:    access.DynamicKindAccess,
		skip:                 skip,
		visit:                visit,
	})
	return res.Coverage, err
}
