package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/skyhook-io/radar/internal/cloud"
)

func TestWebSocketOriginPolicy(t *testing.T) {
	tests := []struct {
		name           string
		server         Server
		host           string
		origin         string
		fetchSite      string
		tls            bool
		forwardedProto string
		want           bool
	}{
		{name: "non-browser client", host: "localhost:9280", want: true},
		{name: "same authority", host: "radar.example.com", origin: "https://radar.example.com", want: true},
		{name: "HTTPS default port on Host", host: "radar.example.com:443", origin: "https://radar.example.com", want: true},
		{name: "HTTPS default port on Origin", host: "radar.example.com", origin: "https://radar.example.com:443", want: true},
		{name: "HTTP default port on Host", host: "radar.example.com:80", origin: "http://radar.example.com", want: true},
		{name: "TLS rejects plaintext origin", host: "radar.example.com", origin: "http://radar.example.com", tls: true, want: false},
		{name: "TLS accepts HTTPS origin", host: "radar.example.com", origin: "https://radar.example.com", tls: true, want: true},
		{name: "TLS proxy rejects plaintext origin", host: "radar.example.com", origin: "http://radar.example.com", forwardedProto: "https", want: false},
		{name: "non-default request port", host: "radar.example.com:8443", origin: "https://radar.example.com", want: false},
		{name: "foreign origin", host: "radar.example.com", origin: "https://evil.example", want: false},
		{name: "same-site foreign origin", host: "radar.example.com", origin: "https://app.example.com", fetchSite: "same-site", want: false},
		{name: "host-rewriting proxy", host: "radar.internal", origin: "https://radar.example.com", fetchSite: "same-origin", want: true},
		{name: "cross-site metadata", host: "radar.example.com", origin: "https://radar.example.com", fetchSite: "cross-site", want: false},
		{name: "Vite development proxy", server: Server{devMode: true}, host: "localhost:9280", origin: "http://localhost:9273", fetchSite: "same-site", want: true},
		{name: "Vite port outside dev mode", host: "localhost:9280", origin: "http://localhost:9273", fetchSite: "same-site", want: false},
		{name: "unrelated development port", server: Server{devMode: true}, host: "localhost:9280", origin: "http://localhost:9274", fetchSite: "same-site", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := "http"
			if tt.tls {
				scheme = "https"
			}
			req := httptest.NewRequest(http.MethodGet, scheme+"://"+tt.host+"/api/pods/ns/pod/exec", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			if tt.forwardedProto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			}
			if got := tt.server.websocketOriginAllowed(req); got != tt.want {
				t.Fatalf("websocketOriginAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpgradeWebSocketUsesServerOriginPolicy(t *testing.T) {
	tests := []struct {
		name      string
		server    Server
		origin    func(string) string
		fetchSite string
		tunnel    bool
		wantOK    bool
	}{
		{name: "same origin", origin: func(serverURL string) string { return serverURL }, wantOK: true},
		{name: "foreign origin", origin: func(string) string { return "https://evil.example" }, wantOK: false},
		{name: "authenticated Hub transport", origin: func(string) string { return "https://hub.example.com" }, fetchSite: "cross-site", tunnel: true, wantOK: true},
		{name: "Vite development proxy", server: Server{devMode: true}, origin: func(string) string { return "http://localhost:9273" }, fetchSite: "same-site", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := tt.server.upgradeWebSocket(w, r)
				if err == nil {
					conn.Close()
				}
			}))
			if tt.tunnel {
				handler = cloud.AuthenticatedTunnelHandler(handler)
			}
			srv := httptest.NewServer(handler)
			defer srv.Close()

			headers := http.Header{"Origin": {tt.origin(srv.URL)}}
			if tt.fetchSite != "" {
				headers.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), headers)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("WebSocket upgrade failed: %v", err)
				}
				conn.Close()
				return
			}
			if err == nil {
				conn.Close()
				t.Fatal("WebSocket upgrade succeeded, want rejection")
			}
			if resp == nil || resp.StatusCode != http.StatusForbidden {
				t.Fatalf("rejection response = %v, want HTTP 403", resp)
			}
		})
	}
}
