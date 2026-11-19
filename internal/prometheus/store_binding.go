package prometheus

import (
	"context"
	"errors"

	"github.com/skyhook-io/radar/pkg/prom"
)

// A view's cluster scope check runs on the client's current connection, while
// its data queries run on a store handle taken earlier. If the connection
// moved between the two (shared store A to single-cluster store B, say), B's
// check could license unfiltered queries that still reach A and mix clusters.
// Such a view takes its handle together with its connection snapshot and
// accepts a check only when that connection is current both before and after
// it; connections only move forward, so that pins the check to the handle's
// backend. A check made on any other connection is the unknown scope, which
// withholds.

var errConnectionReset = errors.New("prometheus connection was reset")

// resolveOnConnection runs resolve, answering the unknown scope unless
// current holds both before and after it.
func resolveOnConnection(current func() bool, resolve func() (ViewScope, error)) (ViewScope, error) {
	if !current() {
		return unknownViewScope(), nil
	}
	scope, err := resolve()
	if err != nil {
		return scope, err
	}
	if !current() {
		return unknownViewScope(), nil
	}
	return scope, nil
}

// boundStore is a store handle with the connection it was taken from. A nil
// handle (no connection when it was taken) fails every query.
type boundStore struct {
	store *prom.Client
	conn  ConnectionSnapshot
}

// bindStore connects if needed and takes the shared store handle with its
// connection. A failed connect leaves the handle nil, so the view's queries
// fail as they would have on the client.
func (c *Client) bindStore(ctx context.Context) boundStore {
	_, _, _ = c.EnsureConnected(ctx)
	store, conn := c.PromSnapshot()
	return boundStore{store: store, conn: conn}
}

func (b boundStore) Query(ctx context.Context, query string) (*prom.QueryResult, error) {
	if b.store == nil {
		return nil, errConnectionReset
	}
	return b.store.Query(ctx, query)
}
