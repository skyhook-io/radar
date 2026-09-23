package resourceid

import "strings"

// GroupKind names a type of object independent of version. The core group is "".
type GroupKind struct {
	Group string
	Kind  string
}

// String renders "Kind" for the core group and "Kind.group" otherwise, the
// form kubectl uses to disambiguate kinds.
func (gk GroupKind) String() string {
	if gk.Group == "" {
		return gk.Kind
	}
	return gk.Kind + "." + gk.Group
}

// Ref is the exact identity of one object within a cluster. Namespace is empty
// for cluster-scoped objects; Group is empty for the core group. A Ref never
// represents an unknown group — use Reference for that.
type Ref struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// NewRef builds a Ref. The group is taken as given; translate a source's own
// spelling of the core group with NormalizeGroup where that source is parsed.
func NewRef(group, kind, namespace, name string) Ref {
	return Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}
}

func (r Ref) Key() string {
	return ResourceKey(r.Group, r.Kind, r.Namespace, r.Name)
}

func (r Ref) GroupKind() GroupKind {
	return GroupKind{Group: r.Group, Kind: r.Kind}
}

// String renders "Kind.group namespace/name" for logs and errors.
func (r Ref) String() string {
	if r.Namespace == "" {
		return r.GroupKind().String() + " " + r.Name
	}
	return r.GroupKind().String() + " " + r.Namespace + "/" + r.Name
}

// NormalizeGroup maps "core" to "", for sources that spell the core group that
// way (Flux inventory IDs, some Gateway API references). Apply it only where
// such a source is parsed: elsewhere "core" is an ordinary group name.
func NormalizeGroup(group string) string {
	if group == "core" {
		return ""
	}
	return group
}

// SplitAPIVersion splits an apiVersion into group and version with the
// Kubernetes rule: no slash means the core group ("v1" → "", "v1"). An empty
// input yields two empty strings.
func SplitAPIVersion(apiVersion string) (group, version string) {
	if before, after, ok := strings.Cut(apiVersion, "/"); ok {
		return before, after
	}
	return "", apiVersion
}

// GroupFromAPIVersion returns the API group of an apiVersion ("apps/v1" →
// "apps", "v1" → ""). An empty apiVersion also yields "", so callers that must
// tell "core" from "not recorded" should check for an empty input or build a
// Reference with ReferenceFromAPIVersion.
func GroupFromAPIVersion(apiVersion string) string {
	group, _ := SplitAPIVersion(apiVersion)
	return group
}

// APIVersion joins a group and version into an apiVersion ("", "v1" → "v1").
func APIVersion(group, version string) string {
	if group == "" {
		return version
	}
	return group + "/" + version
}
