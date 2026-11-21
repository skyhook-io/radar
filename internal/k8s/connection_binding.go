package k8s

import (
	"context"
	"errors"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ErrNoServingConnection means no connection can be admitted against right
// now: a switch, rescope or rebuild is between its point of no return and its
// admission, or Radar is shutting down.
var ErrNoServingConnection = errors.New("no cluster connection is serving")

// BoundConnection is one request's capture of the serving connection: the ID
// it was admitted against and the clients and discovery it uses throughout.
// A switch that happens mid-request replaces the globals, not this capture,
// so the request finishes on the connection it was admitted to.
type BoundConnection struct {
	ID      string
	Context string
	// Config is the installed config object. Installs replace the pointer
	// rather than mutating it, so it stays the admitted connection's.
	Config    *rest.Config
	Client    *kubernetes.Clientset
	Dynamic   dynamic.Interface
	Discovery *ResourceDiscovery
}

// CaptureBoundConnection takes the serving connection's ID, clients and
// discovery in one step. Client installs and the point of no return both run
// under contextOpMu, which CaptureClusterReads holds shared, so the pieces
// always belong to the same admitted connection.
func CaptureBoundConnection() (BoundConnection, error) {
	var b BoundConnection
	if !CaptureClusterReads(func() {
		b.ID = ServingConnectionID()
		clientMu.RLock()
		b.Config, b.Client, b.Dynamic = k8sConfig, k8sClient, dynamicClient
		b.Context = activeClusterContextLocked()
		clientMu.RUnlock()
		b.Discovery = GetResourceDiscovery()
	}) || b.ID == "" || b.Dynamic == nil || b.Config == nil {
		return BoundConnection{}, ErrNoServingConnection
	}
	return b, nil
}

type boundConnectionKey struct{}

// WithBoundConnection attaches a capture to a request context.
func WithBoundConnection(ctx context.Context, b BoundConnection) context.Context {
	return context.WithValue(ctx, boundConnectionKey{}, b)
}

// BoundConnectionFrom returns the capture a request was admitted with.
func BoundConnectionFrom(ctx context.Context) (BoundConnection, bool) {
	b, ok := ctx.Value(boundConnectionKey{}).(BoundConnection)
	return b, ok
}

// discoveryFor resolves kinds with the request's bound discovery when it has
// one, so a bound write never pairs its pinned client with a newer cluster's
// resource mapping.
func discoveryFor(ctx context.Context) *ResourceDiscovery {
	if b, ok := BoundConnectionFrom(ctx); ok {
		return b.Discovery
	}
	return GetResourceDiscovery()
}
