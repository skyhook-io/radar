package cloud

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/hashicorp/yamux"
)

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
	// ctx cancellation.
	srv := &http.Server{Handler: handler}

	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = sess.Close()
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
