package cloud

import (
	"net/http"
	"strings"
	"testing"
)

func TestIsLoopbackHostname(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{host: "localhost", want: true},
		{host: "LOCALHOST", want: true},
		{host: "127.0.0.1", want: true},
		{host: "127.42.0.9", want: true},
		{host: "::1", want: true},
		{host: "localhost.", want: false},
		{host: "localhost.example", want: false},
		{host: "10.0.0.1", want: false},
		{host: "", want: false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			if got := IsLoopbackHostname(tc.host); got != tc.want {
				t.Fatalf("IsLoopbackHostname(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestValidateWebSocketURLTransportPolicy(t *testing.T) {
	for _, raw := range []string{
		"wss://api.radarhq.io/agent",
		"ws://localhost:9091/agent",
		"ws://127.0.0.2:9091/agent",
		"ws://[::1]:9091/agent",
	} {
		t.Run("valid_"+raw, func(t *testing.T) {
			if err := ValidateWebSocketURL(raw); err != nil {
				t.Fatalf("ValidateWebSocketURL(%q): %v", raw, err)
			}
		})
	}

	for _, raw := range []string{
		"ws://api.radarhq.io/agent",
		"ws://10.0.0.1/agent",
		"ws://localhost.example/agent",
		"https://api.radarhq.io/agent",
		"wss://user:password@api.radarhq.io/agent",
		"wss://api.radarhq.io/agent#fragment",
		"wss://api.radarhq.io:0/agent",
		"wss://api.radarhq.io:65536/agent",
		" wss://api.radarhq.io/agent",
	} {
		t.Run("invalid_"+raw, func(t *testing.T) {
			if err := ValidateWebSocketURL(raw); err == nil {
				t.Fatalf("ValidateWebSocketURL(%q) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestValidateHubOriginTransportPolicy(t *testing.T) {
	for _, raw := range []string{
		"https://api.radarhq.io",
		"https://api.radarhq.io/",
		"http://localhost:9091",
		"http://127.0.0.2:9091",
		"http://[::1]:9091",
	} {
		t.Run("valid_"+raw, func(t *testing.T) {
			if err := ValidateHubOrigin(raw); err != nil {
				t.Fatalf("ValidateHubOrigin(%q): %v", raw, err)
			}
		})
	}

	for _, raw := range []string{
		"http://api.radarhq.io",
		"http://10.0.0.1",
		"http://localhost.example",
		"https://api.radarhq.io/api",
		"https://api.radarhq.io?org=test",
		"https://api.radarhq.io:0",
		"https://api.radarhq.io:65536",
	} {
		t.Run("invalid_"+raw, func(t *testing.T) {
			if err := ValidateHubOrigin(raw); err == nil {
				t.Fatalf("ValidateHubOrigin(%q) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestHubOriginFromWebSocketURL(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: "wss://api.radarhq.io/agent", want: "https://api.radarhq.io"},
		{raw: "wss://hub.example:8443/agent?transport=websocket", want: "https://hub.example:8443"},
		{raw: "ws://localhost:9091/agent", want: "http://localhost:9091"},
		{raw: "ws://[::1]:9091/agent", want: "http://[::1]:9091"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := HubOriginFromWebSocketURL(tc.raw)
			if err != nil {
				t.Fatalf("HubOriginFromWebSocketURL(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("HubOriginFromWebSocketURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
	if _, err := HubOriginFromWebSocketURL("ws://api.radarhq.io/agent"); err == nil {
		t.Fatal("plaintext remote WebSocket URL unexpectedly accepted")
	}
}

func TestConfigValidateRejectsPlaintextRemoteCloudURL(t *testing.T) {
	cfg := Config{
		URL:       "ws://api.radarhq.io/agent",
		Token:     "rhc_test",
		ClusterID: "cluster-test",
		Handler:   http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "must use wss://") {
		t.Fatalf("Config.validate() = %v, want TLS policy error", err)
	}
}
