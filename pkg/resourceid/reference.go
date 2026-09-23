package resourceid

// GroupSource records where a Reference's API group came from. It is what
// separates "the core group" from "no group was recorded" — both are "" as a
// string.
type GroupSource uint8

const (
	// GroupMissing: the reference carried no group information at all (an
	// event without apiVersion, a user-typed kind, a legacy persisted value).
	GroupMissing GroupSource = iota
	// GroupObserved: the group was read from the evidence itself — an object's
	// apiVersion, an ownerReference, or a controller's recorded reference such
	// as Argo CD status.resources or a Flux inventory ID. "" is the core group.
	GroupObserved
	// GroupDefaulted: the field was omitted and its API defines a default
	// (Gateway API parentRefs, cert-manager issuerRef, KEDA scaleTargetRef).
	GroupDefaulted
	// GroupRestored: the group was reattached from a known source type, e.g.
	// a typed informer object that arrives without TypeMeta.
	GroupRestored
)

// String names the source for logs and test failures.
func (s GroupSource) String() string {
	switch s {
	case GroupObserved:
		return "observed"
	case GroupDefaulted:
		return "defaulted"
	case GroupRestored:
		return "restored"
	default:
		return "missing"
	}
}

// Reference is a possibly-partial pointer to an object: what one piece of
// evidence says about the object it refers to. It is complete when its group
// source is anything but GroupMissing; resolve it to get a Ref.
type Reference struct {
	Group       string
	GroupSource GroupSource
	Kind        string
	Namespace   string
	Name        string
	// UID identifies the incarnation, when the evidence records one.
	UID string
}

// HasGroup reports whether the reference's group is known (possibly the core
// group), as opposed to missing.
func (r Reference) HasGroup() bool {
	return r.GroupSource != GroupMissing
}

// ObservedReference is a reference whose group was recorded by the evidence
// ("" meaning core), such as Argo CD status.resources or a Flux inventory ID.
func ObservedReference(group, kind, namespace, name string) Reference {
	return Reference{Group: group, GroupSource: GroupObserved, Kind: kind, Namespace: namespace, Name: name}
}

// ReferenceFromAPIVersion builds a reference from an apiVersion field. An
// empty apiVersion means the group was not recorded; "v1" means the core group.
func ReferenceFromAPIVersion(apiVersion, kind, namespace, name string) Reference {
	if apiVersion == "" {
		return UnqualifiedReference(kind, namespace, name)
	}
	return ObservedReference(GroupFromAPIVersion(apiVersion), kind, namespace, name)
}

// OwnerReference builds a reference from an ownerReference. ownerReferences
// carry no namespace: the owner lives in the dependent's namespace, or is
// cluster-scoped, which the caller decides from discovery.
func OwnerReference(apiVersion, kind, name, uid, namespace string) Reference {
	r := ReferenceFromAPIVersion(apiVersion, kind, namespace, name)
	r.UID = uid
	return r
}

// DefaultedReference applies a field's documented default group when the
// field was omitted. A present group is observed, not defaulted — including a
// present empty group, which some APIs (Gateway API refs) use to name the core
// group while defaulting an absent one elsewhere.
func DefaultedReference(group string, present bool, defaultGroup, kind, namespace, name string) Reference {
	if present {
		return ObservedReference(group, kind, namespace, name)
	}
	return Reference{Group: defaultGroup, GroupSource: GroupDefaulted, Kind: kind, Namespace: namespace, Name: name}
}

// RestoredReference reattaches a built-in Kind's group, for evidence known to
// come from that built-in type (typed informer objects). It stays missing for
// kinds that are not built-in.
func RestoredReference(kind, namespace, name string) Reference {
	group, ok := BuiltinGroup(kind)
	if !ok {
		return UnqualifiedReference(kind, namespace, name)
	}
	return Reference{Group: group, GroupSource: GroupRestored, Kind: kind, Namespace: namespace, Name: name}
}

// OptionalGroupReference is for sources whose group field cannot tell the core
// group from an omitted one: an empty group is treated as not recorded.
func OptionalGroupReference(group, kind, namespace, name string) Reference {
	if group == "" {
		return UnqualifiedReference(kind, namespace, name)
	}
	return ObservedReference(group, kind, namespace, name)
}

// UnqualifiedReference is a reference with no group information.
func UnqualifiedReference(kind, namespace, name string) Reference {
	return Reference{GroupSource: GroupMissing, Kind: kind, Namespace: namespace, Name: name}
}
