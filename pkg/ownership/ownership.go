// Package ownership reads what Radar has observed about objects' owners, for
// rules that say what owns an object, what it owns, and what deleting it would
// take with it. A Source never does I/O: answers come from what Radar already
// watches, and every read says whether it was complete.
package ownership

import (
	"errors"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Source opens sessions over one cluster's observations.
type Source interface {
	// Begin opens a session. It does no I/O, starts no watch and waits for
	// nothing.
	Begin() (Session, error)
}

// Session reads observations and records which store served each read, so an
// answer assembled from several reads can be checked for having mixed a store
// with its replacement. A session is not safe for concurrent use.
type Session interface {
	// Catalog is the served API as of Begin. It never changes.
	Catalog() *Catalog
	// Object reads one object. It does no I/O and waits for nothing.
	Object(ref resourceid.Ref) ObjectRead
	// Validate returns nil if every store the session read still serves what
	// it served then; otherwise ErrCachesReplaced or ErrStoreReplaced. An
	// answer built in a session must pass Validate before it is used.
	Validate() error
}

var (
	// ErrNotConnected: there are no caches to read.
	ErrNotConnected = errors.New("ownership: no cluster connection to read")
	// ErrCachesReplaced: the caches were stopped or replaced after the session
	// began, as when Radar switches clusters or rebuilds its cache.
	ErrCachesReplaced = errors.New("ownership: the caches were replaced during the read")
	// ErrStoreReplaced: a store the session read was replaced by another
	// watch, as when a cluster-wide watch takes over from namespace ones.
	ErrStoreReplaced = errors.New("ownership: a store was replaced during the read")
)
