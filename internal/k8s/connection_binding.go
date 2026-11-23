package k8s

import (
	"context"
	"errors"
)

// ErrNoServingConnection means no connection can be admitted against right
// now: a switch, rescope or rebuild is between its point of no return and its
// admission, or Radar is shutting down.
var ErrNoServingConnection = errors.New("no cluster connection is serving")

// Admission is a write's admission: the serving connection and the ID it was
// serving under, read together. A build admitted again after a retirement is
// the same connection under a new ID, and a request admitted before is no
// longer "still bound".
type Admission struct {
	conn *Connection
	id   string
}

// Connection returns the admitted connection.
func (a Admission) Connection() *Connection { return a.conn }

// ID returns the connection ID the write was admitted under.
func (a Admission) ID() string { return a.id }

// Bind binds a request to the admission; ClientFromContext, the Helm write
// path and kind lookups then use its connection's clients and discovery, not
// the installed ones.
func (a Admission) Bind(ctx context.Context) context.Context {
	return context.WithValue(ctx, connectionKey{}, a)
}

// CaptureServingConnection returns the serving connection a write is admitted
// against. Like any capture it is refused while a context operation is queued
// or running: a switch stops sessions before it retires the serving ID, so
// admitting in that window would let a new session slip past the stop.
func CaptureServingConnection() (Admission, error) {
	var a Admission
	if !CaptureClusterReads(func() {
		connectionStatusMu.RLock()
		if serving != nil {
			a = Admission{conn: serving, id: serving.id}
		}
		connectionStatusMu.RUnlock()
	}) || a.conn == nil {
		return Admission{}, ErrNoServingConnection
	}
	return a, nil
}

type connectionKey struct{}

// WithConnection binds a request to c under the ID c holds now, for reads
// that need one connection throughout. Writes bind their Admission instead.
func WithConnection(ctx context.Context, c *Connection) context.Context {
	return Admission{conn: c, id: c.ID()}.Bind(ctx)
}

// ConnectionFrom returns the connection a request was bound to, or nil.
func ConnectionFrom(ctx context.Context) *Connection {
	b, _ := ctx.Value(connectionKey{}).(Admission)
	return b.conn
}

// StillBound reports whether a request's connection is still serving (true for
// an unbound request). Composite services that also read state not yet on the
// Connection (the caches) check it inside their CaptureClusterReads callback.
func StillBound(ctx context.Context) bool {
	b, ok := ctx.Value(connectionKey{}).(Admission)
	connectionStatusMu.RLock()
	defer connectionStatusMu.RUnlock()
	return !ok || b.conn == nil || (b.conn == serving && b.id == servingIDLocked())
}
