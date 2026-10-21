package ownership

import "github.com/skyhook-io/radar/pkg/resourceid"

// The GitOps managers whose status lists the objects they apply. A
// HelmRelease from a helm-controller that predates status.inventory lists
// nothing. A native Helm release keeps its manifest in a release Secret, which
// Radar holds only as a digest; only the claims on its objects name it. Other
// kinds whose status lists what they apply, such as the Flux Operator's
// ResourceSet and FluxInstance, or an Argo CD ApplicationSet's Applications,
// aren't read.
var (
	ArgoApplication   = resourceid.GroupKind{Group: "argoproj.io", Kind: "Application"}
	FluxKustomization = resourceid.GroupKind{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}
	FluxHelmRelease   = resourceid.GroupKind{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}
)

// RecordingManagerKinds returns the manager kinds whose status lists their
// members.
func RecordingManagerKinds() []resourceid.GroupKind {
	return []resourceid.GroupKind{ArgoApplication, FluxHelmRelease, FluxKustomization}
}

// IsRecordingManager reports whether gk's status lists its members.
func IsRecordingManager(gk resourceid.GroupKind) bool {
	return gk == ArgoApplication || gk == FluxHelmRelease || gk == FluxKustomization
}

// MembershipState is what a manager's status says about its members.
type MembershipState uint8

const (
	// MembersUnreadable: the status lists no members Radar can read — none
	// recorded yet, an entry that names no object, or no destination — so any
	// object may be one of them. It is the zero value.
	MembersUnreadable MembershipState = iota
	// MembersRecorded: the status lists the members; the list may be empty.
	MembersRecorded
	// MembersRemote: the manager applies to a destination Radar can't tell
	// is this cluster: another cluster, or this one under another name or
	// address. Its list names objects there.
	MembersRemote
)

func (m MembershipState) String() string {
	switch m {
	case MembersRecorded:
		return "recorded"
	case MembersRemote:
		return "remote"
	default:
		return "unreadable"
	}
}

// MembersRead is one manager's member list.
type MembersRead struct {
	// Manager is the read of the manager itself; the rest is set only when
	// it is Present.
	Manager    ObjectRead
	Membership MembershipState
	// Members, when Recorded or Remote, are the objects the manager lists,
	// sorted, each read as an object of this cluster would be: an object of
	// a cluster-scoped kind has no namespace, whatever the entry records, and
	// an Argo CD entry for a namespaced kind that records none is in the
	// Application's destination namespace. An entry of a kind whose scope
	// Radar can't tell stays as recorded.
	Members []resourceid.Ref
}

// RecordingManagersRead is what a search for the managers recording one object found.
type RecordingManagersRead struct {
	// Managers are the local managers whose member lists name the object,
	// sorted.
	Managers []Observation
	// Remote are the remote managers whose member lists name an object at
	// the same coordinates, sorted: any of them records this one if its
	// destination is this cluster after all.
	Remote []resourceid.Ref
	// Unreadable are the managers whose member list can't be read, any of
	// which may name the object, sorted.
	Unreadable []resourceid.Ref
	// TargetUnresolvable reports that the object can't be named: a reference that
	// contradicts its kind's scope, or a kind whose scope Radar can't tell.
	// No manager's list was searched, and Gaps hold only the object's gap.
	TargetUnresolvable bool
	// Gaps are the manager kinds and namespaces the search couldn't read, or
	// why the object can't be named.
	Gaps []Gap
}

// Complete reports whether Managers are every manager of the
// RecordingManagers kinds recording the object among those whose status this
// cluster stores. A manager of another kind, or one running elsewhere, such
// as a hub's Argo CD or a management cluster's Flux, is invisible here: an
// object with no recording managers may still be managed by GitOps. Complete
// describes Radar's collection, not what a caller may see.
func (r RecordingManagersRead) Complete() bool {
	return len(r.Gaps) == 0 && len(r.Remote) == 0 && len(r.Unreadable) == 0
}
