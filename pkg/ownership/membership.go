package ownership

import "github.com/skyhook-io/radar/pkg/resourceid"

// The GitOps managers whose status lists the objects they apply. A
// HelmRelease from a helm-controller that predates status.inventory lists
// nothing. A native Helm release keeps its manifest in a release Secret, which
// Radar holds only as a digest; only the claims on its objects name it.
var (
	ArgoApplication   = resourceid.GroupKind{Group: "argoproj.io", Kind: "Application"}
	FluxKustomization = resourceid.GroupKind{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}
	FluxHelmRelease   = resourceid.GroupKind{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}
)

// RecordingManagers returns the manager kinds whose status lists their
// members.
func RecordingManagers() []resourceid.GroupKind {
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
	// MembersRemote: the manager applies to another cluster, so the objects
	// it lists aren't objects here.
	MembersRemote
)

func (m MembershipState) String() string {
	return [...]string{"unreadable", "recorded", "remote"}[m]
}

// InventoryRead is one manager's member list.
type InventoryRead struct {
	// Manager is the read of the manager itself; the rest is set only when
	// it is Present.
	Manager    ObjectRead
	Membership MembershipState
	// Members, when Recorded, are the objects the manager lists, sorted, each
	// aligned with its kind's scope as the catalog states it: an object of a
	// cluster-scoped kind has no namespace, whatever the entry records, and
	// an Argo CD entry for a namespaced kind that records none is in the
	// Application's destination namespace. An entry of a kind the catalog
	// doesn't know stays as recorded.
	Members []resourceid.Ref
}

// RecorderRead is what a search for the managers recording one object found.
type RecorderRead struct {
	// Recorders are the local managers whose member lists name the object,
	// sorted.
	Recorders []Observation
	// Unreadable are the local managers whose member list can't be read, any
	// of which may name the object, sorted.
	Unreadable []resourceid.Ref
	// Gaps are the manager kinds and namespaces the search couldn't read. A
	// gap on the object's own kind means the object can't be named: a
	// reference that contradicts its kind's scope, or a kind whose scope
	// Radar can't tell.
	Gaps []Gap
}

// Complete reports whether Recorders are every local manager recording the
// object.
func (r RecorderRead) Complete() bool {
	return len(r.Gaps) == 0 && len(r.Unreadable) == 0
}
