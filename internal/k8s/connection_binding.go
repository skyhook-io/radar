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
	// Config is a copy of the admitted connection's config, safe to modify.
	Config    *rest.Config
	Client    *kubernetes.Clientset
	Dynamic   dynamic.Interface
	Discovery *ResourceDiscovery
}

// CaptureBoundConnection takes the serving connection's ID, clients and
// discovery in one step. A missing client stays nil for the handler to report,
// as it would without binding. Client installs and the point of no return both run
// under contextOpMu, which CaptureClusterReads holds shared, so the pieces
// always belong to the same admitted connection.
func CaptureBoundConnection() (BoundConnection, error) {
	var b BoundConnection
	if !CaptureClusterReads(func() {
		b.ID = ServingConnectionID()
		clientMu.RLock()
		if k8sConfig != nil {
			b.Config = rest.CopyConfig(k8sConfig)
		}
		b.Client, b.Dynamic = k8sClient, dynamicClient
		clientMu.RUnlock()
		// Clients and context change only under the operation lock held here.
		b.Context = ActiveClusterContext()
		b.Discovery = GetResourceDiscovery()
	}) || b.ID == "" {
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

// StillBound reports whether a request is still on the connection it was
// admitted to: true when it carries no binding, or when that connection is
// still serving. Composite services that capture several globals call it
// inside their CaptureClusterReads callback, so the whole capture belongs to
// the bound connection.
func StillBound(ctx context.Context) bool {
	b, ok := BoundConnectionFrom(ctx)
	return !ok || ServingConnectionID() == b.ID
}
