package k8s

import (
	"context"
	"errors"
)

// ErrNoServingConnection means no connection can be admitted against right
// now: a switch, rescope or rebuild is between its point of no return and its
// admission, or Radar is shutting down.
var ErrNoServingConnection = errors.New("no cluster connection is serving")

// CaptureServingConnection returns the serving connection a write is admitted
// against. Like any capture it is refused while a context operation is queued
// or running: a switch stops sessions before it retires the serving ID, so
// admitting in that window would let a new session slip past the stop.
func CaptureServingConnection() (*Connection, error) {
	var c *Connection
	if !CaptureClusterReads(func() {
		connectionStatusMu.RLock()
		c = serving
		connectionStatusMu.RUnlock()
	}) || c == nil {
		return nil, ErrNoServingConnection
	}
	return c, nil
}

type connectionKey struct{}

// boundConnection is a request's binding: the connection, and the admission
// it was bound in. A build admitted again after a retirement is the same
// connection under a new ID; a request bound before is no longer "still bound".
type boundConnection struct {
	conn *Connection
	id   string
}

// WithConnection binds a request to c; ClientFromContext, the Helm write path
// and kind lookups then use c's clients and discovery, not the installed ones.
func WithConnection(ctx context.Context, c *Connection) context.Context {
	return context.WithValue(ctx, connectionKey{}, boundConnection{conn: c, id: c.ID()})
}

// ConnectionFrom returns the connection a request was bound to, or nil.
func ConnectionFrom(ctx context.Context) *Connection {
	b, _ := ctx.Value(connectionKey{}).(boundConnection)
	return b.conn
}

// StillBound reports whether a request's connection is still serving (true for
// an unbound request). Composite services that also read state not yet on the
// Connection (the caches) check it inside their CaptureClusterReads callback.
func StillBound(ctx context.Context) bool {
	b, ok := ctx.Value(connectionKey{}).(boundConnection)
	connectionStatusMu.RLock()
	defer connectionStatusMu.RUnlock()
	return !ok || b.conn == nil || (b.conn == serving && b.id == servingIDLocked())
}
