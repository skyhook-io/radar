// Package cloud connects the Radar binary to a Radar Cloud service.
//
// When configured with a Cloud URL, Radar dials out via WebSocket, establishes
// a yamux session with itself as the server, and serves its existing HTTP
// router over streams that Cloud opens on behalf of browsers. All of
// Radar's endpoints (topology, resources, SSE, pod exec, MCP) work unchanged
// because a yamux stream IS a net.Conn — the router doesn't know the request
// came from a tunnel.
//
// This package is only active when --cloud-url is set. With no Cloud
// configured, Radar's local-binary behavior is unchanged.
package cloud

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// Config is the runtime configuration for connecting to Radar Cloud.
type Config struct {
	// URL is the WebSocket URL of the Cloud service's /agent endpoint,
	// e.g. wss://api.radarhq.io/agent
	URL string

	// Token is the cluster bearer token issued by the Cloud install wizard.
	// Format: rhc_<random>.
	Token string

	// ClusterID stably identifies this cluster to Cloud. Derived from the
	// token on the Cloud side; sent as a query param for clarity and logging.
	ClusterID string

	// ClusterName is the human-readable label the user chose in the wizard.
	ClusterName string

	// Namespace is the Kubernetes namespace Radar is running in. Sent to the
	// hub on connect so it knows where to target Deployment patches on upgrade.
	// Populated from MY_POD_NAMESPACE (downward API); empty string is fine —
	// the hub stores whatever it receives and surfaces a "reconnect required"
	// error if the field is absent when an upgrade is requested.
	Namespace string

	// Handler is the HTTP handler to serve over tunneled streams — typically
	// Radar's Server.Handler() (chi router).
	Handler http.Handler
}

func (c Config) validate() error {
	if c.URL == "" {
		return errors.New("cloud: URL is required")
	}
	if !strings.HasPrefix(c.URL, "ws://") && !strings.HasPrefix(c.URL, "wss://") {
		return errors.New("cloud: URL must start with ws:// or wss://")
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

// Run connects to Radar Cloud and serves incoming streams until ctx is
// cancelled. It reconnects with exponential backoff on disconnect; Run
// returns only on context cancellation or unrecoverable config errors.
func Run(ctx context.Context, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}

	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second
	const warnAfterFailures = 5

	failures := 0

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		log.Printf("[cloud] dialing Radar Cloud: %s cluster=%s", cfg.URL, cfg.ClusterID)
		sess, err := dial(ctx, cfg)
		if err != nil {
			failures++
			log.Printf("[cloud] dial failed: %v (retry in %s)", err, backoff)
			if failures == warnAfterFailures {
				log.Printf("[cloud] WARN: %d consecutive failures — verify --cloud-url, --cloud-token, and --cluster-name", failures)
			}
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}
		failures = 0

		log.Printf("[cloud] connected to Radar Cloud; serving streams")
		connectedAt := time.Now()

		err = serve(ctx, sess, cfg.Handler)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[cloud] session ended: %v", err)
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Only reset backoff if the session stayed up long enough to count
		// as healthy — otherwise a Cloud that accepts-then-immediately-kills
		// the stream causes a tight dial→die→dial loop. Sleep before the
		// next dial in the short-session case.
		if time.Since(connectedAt) >= 30*time.Second {
			backoff = 1 * time.Second
		} else {
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff, maxBackoff)
		}
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
