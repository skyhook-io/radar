package cloud

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/hashicorp/yamux"
)

// authenticatedTunnelRequestKey is deliberately private. A browser or pod can
// copy every HTTP header the Hub sends, but it cannot manufacture this
// in-process context value. Only the handler attached to the cluster-token-
// authenticated yamux session marks requests with it.
type authenticatedTunnelRequestKey struct{}

// IsAuthenticatedTunnelRequest reports whether r arrived over the authenticated
// Hub yamux session. Cloud proxy auth uses this in addition to the forwarded
// identity headers so a pod that reaches Radar's ordinary TCP listener cannot
// turn spoofed headers into Kubernetes impersonation credentials.
func IsAuthenticatedTunnelRequest(ctx context.Context) bool {
	marked, _ := ctx.Value(authenticatedTunnelRequestKey{}).(bool)
	return marked
}

// AuthenticatedTunnelHandler marks requests as originating from the
// cluster-token-authenticated Hub tunnel. Only the tunnel transport should wrap
// a handler with it; serve applies it to every accepted yamux stream.
func AuthenticatedTunnelHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), authenticatedTunnelRequestKey{}, true)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// serve accepts yamux streams and hands them to http.Serve using Radar's
// existing HTTP handler. Each accepted stream is a net.Conn; http.Serve
// reads one HTTP request (or WebSocket upgrade) per connection. Long-lived
// responses (SSE, exec WebSocket) stay on the same stream until the browser
// disconnects.
//
// Returns when the yamux session closes or ctx is cancelled.
func serve(ctx context.Context, sess *yamux.Session, handler http.Handler) error {
	listener := &yamuxListener{sess: sess}

	// Use http.Server rather than http.Serve so we can cleanly shut down on
	// ctx cancellation. Marking happens here, at the transport boundary: the
	// same Radar router is also attached to a local TCP listener, so marking it
	// inside the router would let direct in-cluster callers spoof Cloud identity.
	srv := &http.Server{Handler: AuthenticatedTunnelHandler(handler)}

	// The watcher exits either on ctx cancel (which triggers shutdown) or
	// when Serve returns on its own — the `done` channel prevents the
	// goroutine from leaking across many reconnects if the session dies
	// before ctx is cancelled.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = srv.Close()
			_ = sess.Close()
		case <-done:
		}
	}()

	err := srv.Serve(listener)
	// net.ErrClosed is the expected outcome when the session closes or ctx
	// cancels. Surface other errors as real.
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) || errors.Is(err, yamux.ErrSessionShutdown) {
		return nil
	}
	return err
}

// yamuxListener adapts a yamux.Session to net.Listener so http.Server can
// accept streams as if they were incoming TCP connections.
type yamuxListener struct {
	sess *yamux.Session
}

func (l *yamuxListener) Accept() (net.Conn, error) {
	stream, err := l.sess.AcceptStream()
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (l *yamuxListener) Close() error   { return l.sess.Close() }
func (l *yamuxListener) Addr() net.Addr { return l.sess.LocalAddr() }

var _ net.Listener = (*yamuxListener)(nil)
