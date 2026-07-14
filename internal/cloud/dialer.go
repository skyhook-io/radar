package cloud

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

const selfUpgradeAvailableHeader = "X-Radar-Self-Upgrade-Available"

// dial establishes a WebSocket to Radar Cloud, authenticates with the
// cluster bearer token, and returns a yamux session with this side as the
// *server*. Cloud opens streams (one per browser request); we accept them.
func dial(ctx context.Context, cfg Config) (*yamux.Session, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse cloud URL: %w", err)
	}
	q := u.Query()
	q.Set("cluster_id", cfg.ClusterID)
	q.Set("cluster_name", cfg.ClusterName)
	u.RawQuery = q.Encode()

	headers := cloudHandshakeHeaders(cfg)

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	ws, resp, err := dialer.DialContext(ctx, u.String(), headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return nil, fmt.Errorf("Radar Cloud rejected token (401) — check --cloud-token")
			case http.StatusForbidden:
				return nil, fmt.Errorf("Radar Cloud rejected cluster (403) — token may be revoked or cluster disabled")
			case http.StatusNotFound:
				return nil, fmt.Errorf("Radar Cloud endpoint not found (404) — check --cloud-url path")
			default:
				return nil, fmt.Errorf("Radar Cloud rejected connection: status=%d: %w", resp.StatusCode, err)
			}
		}
		return nil, fmt.Errorf("ws dial: %w", err)
	}

	// We are the yamux *server* (accepts streams). Cloud is the client
	// (opens streams when browser requests arrive).
	mux, err := yamux.Server(newWSConn(ws), tunnelYamuxConfig())
	if err != nil {
		ws.Close()
		return nil, fmt.Errorf("yamux server setup: %w", err)
	}
	return mux, nil
}

// cloudHandshakeHeaders builds the metadata Radar advertises when opening a
// Cloud tunnel. The self-upgrade capability is deliberately always present:
// false is meaningful for GitOps and --no-self-upgrade installations and must
// not be confused with an older agent that predates the capability contract.
func cloudHandshakeHeaders(cfg Config) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+cfg.Token)
	headers.Set("X-Radar-Version", Version)
	headers.Set(selfUpgradeAvailableHeader, strconv.FormatBool(cfg.SelfUpgradeAvailable))
	if cfg.Namespace != "" {
		headers.Set("X-Radar-Namespace", cfg.Namespace)
	}
	// Validate before send — the value comes from a ConfigMap on the
	// cluster, and a corrupted ConfigMap shouldn't be able to inject
	// header smuggling. Reject silently on bad shape; hub falls back
	// to name-based correlation.
	if apiURL, err := validateAPIServerURL(cfg.APIServerURL); err == nil && apiURL != "" {
		headers.Set("X-Radar-API-Server-URL", apiURL)
	}
	return headers
}

// tunnelYamuxConfig is the yamux config for the Cloud tunnel. It differs from
// yamux's defaults only in MaxStreamWindowSize.
//
// Per-stream throughput over yamux is capped at window/RTT, and yamux v0.1.2 has
// no RTT-based window auto-tuning, so the ceiling is committed per-stream and
// must be set statically. yamux's 256KB default throttles a single stream to
// under 2MB/s across an intercontinental hop (100-200ms RTT); 4MB lifts that to
// ~27MB/s at 150ms RTT.
//
// This is our *receive* window — it governs the hub→agent direction (request
// bodies, exec stdin, apply payloads), which is small, so the value is not
// load-bearing here. The bulk path is responses (agent→hub), gated by the hub's
// own window (radar-hub tunnelYamuxConfig). We keep this side aligned at 4MB for
// symmetry; the customer binary is single-tenant, so the per-stream buffer cost
// is negligible.
func tunnelYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.MaxStreamWindowSize = 4 << 20 // 4MB
	return cfg
}
