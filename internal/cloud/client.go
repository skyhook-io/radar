// Package cloud connects the Radar binary to a Radar Hub instance.
//
// When configured with a hub URL, Radar dials out via WebSocket, establishes
// a yamux session with itself as the server, and serves its existing HTTP
// router over streams that the hub opens on behalf of browsers. All of
// Radar's endpoints (topology, resources, SSE, pod exec, MCP) work unchanged
// because a yamux stream IS a net.Conn — the router doesn't know the request
// came from a tunnel.
//
// This package is only active when --hub-url is set. With no hub configured,
// Radar's local-binary behavior is unchanged.
package cloud

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

// Config is the runtime configuration for connecting to a Radar Hub.
type Config struct {
	// HubURL is the WebSocket URL of the hub's /agent endpoint,
	// e.g. wss://api.radar.skyhook.io/agent
	HubURL string

	// Token is the cluster bearer token issued by the hub install wizard.
	// Format: rhc_<random>.
	Token string

	// ClusterID stably identifies this cluster in the hub. Derived from the
	// token on the hub side; sent as a query param for clarity and logging.
	ClusterID string

	// ClusterName is the human-readable label the user chose in the wizard.
	ClusterName string

	// Handler is the HTTP handler to serve over tunneled streams — typically
	// Radar's Server.Handler() (chi router).
	Handler http.Handler
}

func (c Config) validate() error {
	if c.HubURL == "" {
		return errors.New("cloud: HubURL is required")
	}
	if c.Token == "" {
		return errors.New("cloud: Token is required")
	}
	if c.ClusterID == "" {
		return errors.New("cloud: ClusterID is required")
	}
	if c.Handler == nil {
		return errors.New("cloud: Handler is required")
	}
	return nil
}

// Run connects to the hub and serves incoming streams until ctx is cancelled.
// It reconnects with exponential backoff on disconnect; Run returns only on
// context cancellation or unrecoverable config errors.
func Run(ctx context.Context, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}

	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		log.Printf("[cloud] dialing hub: %s cluster=%s", cfg.HubURL, cfg.ClusterID)
		sess, err := dial(ctx, cfg)
		if err != nil {
			log.Printf("[cloud] dial failed: %v (retry in %s)", err, backoff)
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}

		log.Printf("[cloud] connected to hub; serving streams")
		backoff = 1 * time.Second // reset on successful connect

		err = serve(ctx, sess, cfg.Handler)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[cloud] session ended: %v", err)
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Reconnect.
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	n := cur * 2
	if n > max {
		n = max
	}
	return n
}
