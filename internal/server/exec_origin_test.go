package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/skyhook-io/radar/internal/cloud"
)

func TestCheckWebSocketOriginTrustsAuthenticatedTunnel(t *testing.T) {
	var got bool
	handler := cloud.AuthenticatedTunnelHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = checkWebSocketOrigin(r)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://radar.internal/api/pods/ns/pod/exec", nil)
	req.Header.Set("Origin", "https://hub.example.com")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !got {
		t.Error("checkWebSocketOrigin refused an authenticated-tunnel request; Hub-proxied exec would break")
	}
}

func TestCheckWebSocketOriginUsesFetchMetadataThroughHostRewritingProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://radar.internal/api/pods/ns/pod/exec", nil)
	req.Header.Set("Origin", "https://radar.example.com")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	if !checkWebSocketOrigin(req) {
		t.Error("same-origin browser request was rejected after the proxy rewrote Host")
	}
}

func TestCheckWebSocketOriginRejectsCrossSiteFetch(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://radar.example.com/api/pods/ns/pod/exec", nil)
	req.Header.Set("Origin", "http://radar.example.com")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	if checkWebSocketOrigin(req) {
		t.Error("cross-site browser request was accepted despite Fetch Metadata")
	}
}

func TestWebSocketOriginAllowsOnlyTheViteDevCrossPort(t *testing.T) {
	for _, hostname := range []string{"localhost", "radar.localhost"} {
		t.Run(hostname, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+hostname+":9280/api/pods/ns/pod/exec", nil)
			req.Header.Set("Origin", "http://"+hostname+":9273")
			req.Header.Set("Sec-Fetch-Site", "same-site")

			if checkWebSocketOrigin(req) {
				t.Error("production origin policy accepted a loopback cross-port request")
			}
			if !(&Server{devMode: true}).websocketOriginAllowed(req) {
				t.Error("Vite development origin was rejected in dev mode")
			}
			if (&Server{}).websocketOriginAllowed(req) {
				t.Error("Vite development origin was accepted outside dev mode")
			}

			req.Header.Set("Origin", "http://"+hostname+":9274")
			if (&Server{devMode: true}).websocketOriginAllowed(req) {
				t.Error("unrelated loopback origin was accepted in dev mode")
			}
		})
	}
}

func TestWebSocketOriginRejectsCrossSiteMetadataForVitePort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://localhost:9280/api/pods/ns/pod/exec", nil)
	req.Header.Set("Origin", "http://localhost:9273")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	if (&Server{devMode: true}).websocketOriginAllowed(req) {
		t.Error("cross-site request was accepted through the Vite development exception")
	}
}

func TestCheckWebSocketOriginRejectsNonLoopbackSameSiteRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://radar.example.com/api/pods/ns/pod/exec", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Sec-Fetch-Site", "same-site")

	if checkWebSocketOrigin(req) {
		t.Error("cross-origin same-site browser request was accepted")
	}
}

func TestCheckWebSocketOriginAcceptsSameAuthorityWithSameSiteMetadata(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://radar.example.com/api/pods/ns/pod/exec", nil)
	req.Header.Set("Origin", "https://radar.example.com")
	req.Header.Set("Sec-Fetch-Site", "same-site")

	if !checkWebSocketOrigin(req) {
		t.Error("same-authority browser request was rejected because Fetch Metadata reported same-site")
	}
}

func TestUpgraderRejectsCrossOriginHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	sameHost := strings.TrimPrefix(srv.URL, "http://")

	t.Run("cross origin rejected", func(t *testing.T) {
		_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"http://evil.example"}})
		if err == nil {
			t.Fatal("cross-origin handshake succeeded, want rejection")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("cross-origin handshake status = %v, want 403", resp)
		}
	})

	t.Run("same origin accepted", func(t *testing.T) {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"http://" + sameHost}})
		if err != nil {
			t.Fatalf("same-origin handshake failed: %v", err)
		}
		conn.Close()
	})
}
