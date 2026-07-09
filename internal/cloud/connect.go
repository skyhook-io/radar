package cloud

// Cloud Connect device-flow client (RFC 8628-shaped). This is the Radar side of
// the flow the hub implements: Radar POSTs a connect request, shows the user the
// approval URL, the user approves in the browser, and Radar polls with its
// device_secret until it receives the minted cluster token. See the hub's
// connect_handlers.go and docs/OSS-TO-CLOUD-UX.md §3 for the contract.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ConnectMetadata is the display context Radar advertises about the cluster it
// wants to connect. All fields are best-effort; the consent page renders what's
// present. None is security-relevant.
type ConnectMetadata struct {
	DeploymentMode string `json:"deployment_mode,omitempty"` // "local" | "in-cluster"
	ClusterName    string `json:"cluster_name,omitempty"`
	RadarVersion   string `json:"radar_version,omitempty"`
	K8sVersion     string `json:"k8s_version,omitempty"`
	K8sDistro      string `json:"k8s_distro,omitempty"`
	NodeCount      *int   `json:"node_count,omitempty"`
	Scope          string `json:"scope,omitempty"`
}

// CreateResponse is the hub's reply to POST /api/connect/requests.
type CreateResponse struct {
	RequestID    string `json:"request_id"`
	DeviceSecret string `json:"device_secret"`
	ConnectURL   string `json:"connect_url"`
	ExpiresIn    int    `json:"expires_in"`
	PollInterval int    `json:"poll_interval"`
	WSSURL       string `json:"wss_url"`
}

// PollResponse is the hub's reply to GET /api/connect/requests/{id}.
type PollResponse struct {
	Status    string `json:"status"` // pending | approved | consumed | expired
	ClusterID string `json:"cluster_id,omitempty"`
	Token     string `json:"token,omitempty"`
	WSSURL    string `json:"wss_url,omitempty"`
}

// ConnectClient talks to a hub's connect endpoints.
type ConnectClient struct {
	// HubBase is the hub API origin, e.g. https://api.radarhq.io (no trailing /).
	HubBase string
	HTTP    *http.Client
}

// NewConnectClient returns a client for the given hub origin.
func NewConnectClient(hubBase string) *ConnectClient {
	return &ConnectClient{
		HubBase: strings.TrimRight(hubBase, "/"),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Create initiates a connect request. The returned device_secret is the
// credential Radar uses to poll; it is never sent in a URL.
func (c *ConnectClient) Create(ctx context.Context, meta ConnectMetadata) (*CreateResponse, error) {
	body, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.HubBase+"/api/connect/requests", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching %s: %w", c.HubBase, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("create connect request: hub returned %s", resp.Status)
	}
	var out CreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding hub response: %w", err)
	}
	if out.RequestID == "" || out.DeviceSecret == "" || out.ConnectURL == "" {
		return nil, errors.New("hub response missing required fields")
	}
	return &out, nil
}

// Poll checks the status of a connect request, authenticating with the
// device_secret. A non-2xx (e.g. 401) is a terminal error — the caller stops.
func (c *ConnectClient) Poll(ctx context.Context, requestID, deviceSecret string) (*PollResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.HubBase+"/api/connect/requests/"+requestID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+deviceSecret)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("poll rejected (401) — the connect request is no longer valid")
	}
	if resp.StatusCode != http.StatusOK {
		// Status only — never echo the hub's response body into an error that
		// gets printed: this request carried the device_secret in the auth
		// header, and a diagnostic hub/proxy that reflected headers could
		// otherwise surface it.
		return nil, fmt.Errorf("poll: hub returned %s", resp.Status)
	}
	var out PollResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding poll response: %w", err)
	}
	return &out, nil
}

// FlowResult is what a completed connect flow yields.
type FlowResult struct {
	ClusterID   string
	Token       string
	WSSURL      string
	ClusterName string
}

// ErrConnectExpired is returned by RunFlow when the request expires before the
// user approves it.
var ErrConnectExpired = errors.New("connect request expired before approval")

// RunFlow drives the whole browser device flow to completion: create the
// request, present the approval URL (and open the browser unless suppressed),
// then poll until approved. It writes user-facing progress to out.
// openBrowser is called with the connect URL unless nil.
func (c *ConnectClient) RunFlow(ctx context.Context, meta ConnectMetadata, out io.Writer, openBrowser func(string)) (*FlowResult, error) {
	cr, err := c.Create(ctx, meta)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(out, "\n  Approve this connection in your browser:\n\n    %s\n\n", cr.ConnectURL)
	if openBrowser != nil {
		openBrowser(cr.ConnectURL)
	}
	fmt.Fprintf(out, "  Waiting for approval… (Ctrl-C to cancel)\n")

	pr, err := c.PollUntilApproved(ctx, cr)
	if err != nil {
		return nil, err
	}
	wss := pr.WSSURL
	if wss == "" {
		wss = cr.WSSURL
	}
	// Require everything needed to actually dial — a token-less or URL-less
	// "approved" would print "Connected" but silently serve nothing (main skips
	// the dial when --cloud-url is empty).
	if pr.Token == "" || pr.ClusterID == "" || wss == "" {
		return nil, errors.New("hub approved the connection but returned incomplete details (missing token, cluster id, or URL)")
	}
	return &FlowResult{
		ClusterID:   pr.ClusterID,
		Token:       pr.Token,
		WSSURL:      wss,
		ClusterName: meta.ClusterName,
	}, nil
}

// PollUntilApproved polls the connect request until it reaches a terminal state,
// honoring the hub-advertised poll interval (clamped to a sane 2–30s band) and
// the request's expiry (floored at 60s). It is the single poll loop shared by
// RunFlow (radar cloud connect) and the in-cluster install driver so their
// interval, expiry, and terminal-state semantics can't drift apart.
//
// It polls immediately (catching an approval that landed during browser-open),
// then waits between polls without ever sleeping past the deadline. Returns the
// approved PollResponse, ErrConnectExpired on expiry, or an error on a consumed
// request / transport failure / context cancellation.
func (c *ConnectClient) PollUntilApproved(ctx context.Context, cr *CreateResponse) (*PollResponse, error) {
	// Clamp the hub-advertised interval — don't trust a value that's implausibly
	// small (busy-poll) or large (never checks before TTL); treat ≤0 as unset.
	interval := time.Duration(cr.PollInterval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if interval < 2*time.Second {
		interval = 2 * time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	deadline := time.Now().Add(time.Duration(max(cr.ExpiresIn, 60)) * time.Second)

	for {
		pr, err := c.Poll(ctx, cr.RequestID, cr.DeviceSecret)
		if err != nil {
			return nil, err
		}
		switch pr.Status {
		case "approved":
			return pr, nil
		case "consumed":
			// The token was already delivered + the cluster connected on a prior
			// run. A fresh flow can't retrieve it; the user re-runs.
			return nil, errors.New("this connect request was already used — run connect again")
		case "expired":
			return nil, ErrConnectExpired
		}
		// pending — wait, but never sleep past the deadline (a large interval
		// mustn't stretch the wait beyond expires_in).
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, ErrConnectExpired
		}
		wait := interval
		if remaining < wait {
			wait = remaining
		}
		if !sleep(ctx, wait) {
			return nil, ctx.Err()
		}
	}
}
