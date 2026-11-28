// Package kargo interprets Kargo (kargo.akuity.io) objects without I/O,
// caches or caller permissions. Every reader takes the unstructured objects a
// host already holds, so moving from Kargo's v1.12 CRDs to its REST API later
// changes the inputs here, not the hosts.
//
// Field paths follow the v1alpha1 API of Kargo v1.12. The only fields that
// Kargo plans to move out of the CRDs are Freight.status.currentlyIn,
// verifiedIn and approvedFor; they are read only in freight_status.go.
package kargo

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Group is Kargo's API group. Kind names such as Stage, Project and
// Promotion collide with other tools, so every reader matches the group too.
const Group = "kargo.akuity.io"

const (
	KindProject   = "Project"
	KindWarehouse = "Warehouse"
	KindFreight   = "Freight"
	KindStage     = "Stage"
	KindPromotion = "Promotion"
)

const (
	// AliasLabel holds a Freight's alias when the alias field is empty.
	AliasLabel = "kargo.akuity.io/alias"
	// ArgoCDContextAnnotation on a Stage is a JSON list of the Argo CD
	// Applications its last promotion updated or waited on.
	ArgoCDContextAnnotation = "kargo.akuity.io/argocd-context"
	// AuthorizedStageAnnotation on an Argo CD Application lists the Stages
	// allowed to update it. It is permission, not evidence of a promotion.
	AuthorizedStageAnnotation = "kargo.akuity.io/authorized-stage"
)

// GroupKind is the Kargo kind's group-qualified identity.
func GroupKind(kind string) resourceid.GroupKind {
	return resourceid.GroupKind{Group: Group, Kind: kind}
}

// Is reports whether u is the Kargo kind, by exact API group.
func Is(u *unstructured.Unstructured, kind string) bool {
	return u != nil && u.GetKind() == kind && resourceid.GroupFromAPIVersion(u.GetAPIVersion()) == Group
}

// Origin is where Freight comes from. Kargo v1.12 has one origin kind,
// Warehouse.
type Origin struct {
	Kind string
	Name string
}

// String is Kargo's own spelling, "Warehouse/name", which keys
// Stage.status.freightHistory[].items and Freight status maps.
func (o Origin) String() string {
	if o.Kind == "" && o.Name == "" {
		return ""
	}
	return o.Kind + "/" + o.Name
}

func parseOrigin(m map[string]any) Origin {
	return Origin{Kind: str(m, "kind"), Name: str(m, "name")}
}

func str(m map[string]any, fields ...string) string {
	s, _, _ := unstructured.NestedString(m, fields...)
	return s
}

func boolean(m map[string]any, fields ...string) bool {
	b, _, _ := unstructured.NestedBool(m, fields...)
	return b
}

func object(m map[string]any, fields ...string) map[string]any {
	o, _, _ := unstructured.NestedMap(m, fields...)
	return o
}

// objects returns the map entries of a list, skipping entries of other types.
func objects(m map[string]any, fields ...string) []map[string]any {
	raw, _, _ := unstructured.NestedFieldNoCopy(m, fields...)
	list, _ := raw.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if o, ok := item.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

// integer reads a number whether it decoded as an integer (the API's JSON
// decoder) or a float (YAML and plain JSON decoders).
func integer(m map[string]any, fields ...string) int64 {
	raw, _, _ := unstructured.NestedFieldNoCopy(m, fields...)
	switch n := raw.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func stringList(m map[string]any, fields ...string) []string {
	s, _, _ := unstructured.NestedStringSlice(m, fields...)
	return s
}

// timeAt parses an RFC 3339 timestamp; the zero time means absent or malformed.
func timeAt(m map[string]any, fields ...string) time.Time {
	raw := str(m, fields...)
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

// duration parses a metav1.Duration string such as "1h30m".
func duration(m map[string]any, fields ...string) (time.Duration, bool) {
	raw := str(m, fields...)
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false
	}
	return d, true
}
