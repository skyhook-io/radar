// Package ownership reads what Radar has observed about objects' owners, for
// rules that say what owns an object, what it owns, and what deleting it would
// take with it. A Source never does I/O: answers come from what Radar already
// watches, and every read says whether it was complete.
package ownership

import (
	"errors"

	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Source opens sessions over one cluster's observations.
type Source interface {
	// Begin opens a session. It does no I/O, starts no watch and waits for
	// nothing.
	Begin() (ReadSession, error)
}

// ReadSession reads observations from the caches current when it began. Reads
// are not a snapshot: each sees its store as it is when read, as fresh as the
// informer behind it, so an answer should read each object once. A session is
// not safe for concurrent use.
//
// A session does no authorization: it reads everything Radar collected,
// whoever asks, and its coverage describes Radar's collection, not what a
// caller may see. A host decides what its caller may read before it shows an
// answer; ValidateCaches says the reads came from the current caches, not that
// anyone may see them.
type ReadSession interface {
	// Catalog is the served API as of Begin. It never changes.
	Catalog() *Catalog
	// Object reads one object. It does no I/O and waits for nothing.
	Object(ref resourceid.Ref) ObjectRead
	// DependentCandidates finds the objects of the catalog's dependent kinds in
	// domain whose owner references name uid, at most limit of them (0 for
	// no limit). The limit caps what it returns, not the search: it gathers
	// every match first. It does no I/O and waits for nothing.
	DependentCandidates(uid types.UID, domain SearchScope, limit int) DependentCandidatesRead
	// Members reads the member list in a manager's status; a manager must
	// be of a RecordingManagers kind. It does no I/O and waits for nothing.
	Members(manager resourceid.Ref) MembersRead
	// RecordingManagers finds the local managers, in every namespace, whose
	// member lists name ref, whatever namespaces the caller may read. It does no
	// I/O and waits for nothing.
	RecordingManagers(ref resourceid.Ref) RecordingManagersRead
	// ValidateCaches returns ErrCachesReplaced if the caches the session read
	// were stopped or replaced since it began, as when Radar switches clusters
	// or rebuilds its cache, and nil otherwise. An answer built in a session
	// must pass ValidateCaches before it is used: one read across a switch may
	// mix two clusters. A store replaced within the caches, such as namespace
	// watches giving way to a cluster-wide one, needs no check: what it
	// answered was true when read, as with any informer's lag.
	ValidateCaches() error
}

var (
	// ErrNotConnected: there are no caches to read.
	ErrNotConnected = errors.New("ownership: no cluster connection to read")
	// ErrCachesReplaced: the caches were stopped or replaced after the session
	// began, as when Radar switches clusters or rebuilds its cache. A cache
	// that was missing when the session began and is there now, as while
	// Radar starts, replaced nothing the session read.
	ErrCachesReplaced = errors.New("ownership: the caches were replaced during the read")
)
