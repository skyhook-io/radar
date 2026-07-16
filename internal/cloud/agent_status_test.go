package cloud

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAgentStatusClientGet(t *testing.T) {
	c, err := NewAgentStatusClient("wss://api.radarhq.io/agent")
	if err != nil {
		t.Fatalf("NewAgentStatusClient: %v", err)
	}
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.radarhq.io/api/agent/status" {
			t.Fatalf("URL = %q", r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer rhc_secret" {
			t.Fatalf("Authorization = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"cluster_id":"clus_123","status":"connected","last_connected_at":"2026-07-18T07:30:00Z"}`)),
		}, nil
	})}

	got, err := c.Get(context.Background(), "rhc_secret")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ClusterID != "clus_123" || got.Status != "connected" || got.LastConnectedAt == nil || got.LastConnectedAt.UTC().Format("2006-01-02T15:04:05Z") != "2026-07-18T07:30:00Z" {
		t.Fatalf("status = %#v", got)
	}
}

func TestAgentStatusClientRejectsUnauthorizedWithoutResponseBodyLeak(t *testing.T) {
	c, err := NewAgentStatusClient("wss://api.radarhq.io/agent")
	if err != nil {
		t.Fatalf("NewAgentStatusClient: %v", err)
	}
	c.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("echoed rhc_secret")),
		}, nil
	})}

	_, err = c.Get(context.Background(), "rhc_secret")
	if !errors.Is(err, ErrAgentStatusUnauthorized) {
		t.Fatalf("Get error = %v", err)
	}
	if strings.Contains(err.Error(), "rhc_secret") {
		t.Fatalf("Get error exposed credential: %v", err)
	}
}

func TestAgentStatusClientRecognizesMissingStatusEndpoint(t *testing.T) {
	c, err := NewAgentStatusClient("wss://acme.radarhq.io/agent")
	if err != nil {
		t.Fatalf("NewAgentStatusClient: %v", err)
	}
	c.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("not found")),
		}, nil
	})}

	_, err = c.Get(context.Background(), "rhc_secret")
	if !errors.Is(err, ErrAgentStatusEndpointNotFound) {
		t.Fatalf("Get error = %v", err)
	}
}

func TestAgentStatusClientRejectsRedirects(t *testing.T) {
	c, err := NewAgentStatusClient("wss://api.radarhq.io/agent")
	if err != nil {
		t.Fatalf("NewAgentStatusClient: %v", err)
	}
	c.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Status:     "307 Temporary Redirect",
			Header:     http.Header{"Location": []string{"https://attacker.example/status"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}

	if _, err := c.Get(context.Background(), "rhc_secret"); err == nil || !strings.Contains(err.Error(), "redirects are not allowed") {
		t.Fatalf("Get error = %v", err)
	}
}
