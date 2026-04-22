package cloud

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

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

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+cfg.Token)

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
	mux, err := yamux.Server(newWSConn(ws), yamux.DefaultConfig())
	if err != nil {
		ws.Close()
		return nil, fmt.Errorf("yamux server setup: %w", err)
	}
	return mux, nil
}
