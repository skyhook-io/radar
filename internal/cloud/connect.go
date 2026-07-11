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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxConnectErrorBodyBytes = 8 << 10
	connectErrorRedaction    = "[REDACTED]"
	connectFinalPollTimeout  = 3 * time.Second
)

type connectHTTPError struct {
	operation     string
	statusCode    int
	status        string
	body          string
	retryAfter    time.Duration
	hasRetryAfter bool
}

func (e *connectHTTPError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("%s: hub returned %s", e.operation, e.status)
	}
	return fmt.Sprintf("%s: hub returned %s: %s", e.operation, e.status, e.body)
}

type pollTransportError struct {
	err error
}

func (e *pollTransportError) Error() string { return e.err.Error() }
func (e *pollTransportError) Unwrap() error { return e.err }

// ConnectMetadata is the display context Radar advertises about the cluster it
// wants to connect. DeploymentMode is required; the consent-page metadata is
// best-effort and none of it is security-relevant.
type ConnectMetadata struct {
	DeploymentMode string `json:"deployment_mode"` // "in-cluster"
	ClusterName    string `json:"cluster_name,omitempty"`
	RadarVersion   string `json:"radar_version,omitempty"`
	K8sVersion     string `json:"k8s_version,omitempty"`
	K8sDistro      string `json:"k8s_distro,omitempty"`
	NodeCount      *int   `json:"node_count,omitempty"`
	Scope          string `json:"scope,omitempty"`
}

// CreateResponse is the hub's reply to POST /api/connect/requests.
type CreateResponse struct {
	RequestID            string `json:"request_id"`
	DeviceSecret         string `json:"device_secret"`
	ConnectURL           string `json:"connect_url"`
	ExpiresIn            int    `json:"expires_in"`
	TokenPickupExpiresIn int    `json:"token_pickup_expires_in"`
	PollInterval         int    `json:"poll_interval"`
	WSSURL               string `json:"wss_url"`

	recoveryDeadline time.Time
}

// PollResponse is the hub's reply to GET /api/connect/requests/{id}.
type PollResponse struct {
	Status    string `json:"status"` // pending | approved | consumed | expired | pickup_expired | rejected
	ClusterID string `json:"cluster_id,omitempty"`
	Token     string `json:"token,omitempty"`
	WSSURL    string `json:"wss_url,omitempty"`
}

// ConnectClient talks to a hub's connect endpoints.
type ConnectClient struct {
	// HubBase is the hub API origin, e.g. https://api.radarhq.io (no trailing /).
	HubBase                 string
	HTTP                    *http.Client
	pollRetryInitialBackoff time.Duration
}

// NewConnectClient returns a client for the given hub origin.
func NewConnectClient(hubBase string) *ConnectClient {
	return &ConnectClient{
		HubBase:                 strings.TrimRight(hubBase, "/"),
		HTTP:                    &http.Client{Timeout: 30 * time.Second},
		pollRetryInitialBackoff: time.Second,
	}
}

func (c *ConnectClient) validateHubOrigin() error {
	if err := ValidateHubOrigin(c.HubBase); err != nil {
		return fmt.Errorf("invalid Hub API origin: %w", err)
	}
	return nil
}

// do rejects every redirect. These endpoints have fixed paths, and following a
// redirect could carry the device-secret Authorization header from HTTPS to a
// plaintext destination. Copying the client preserves injected transports and
// test clients while making the policy non-optional at the request boundary.
func (c *ConnectClient) do(req *http.Request) (*http.Response, error) {
	if c.HTTP == nil {
		return nil, errors.New("Hub HTTP client is required")
	}
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("Hub API redirects are not allowed")
	}
	return client.Do(req)
}

// Create initiates a connect request. The returned device_secret is the
// credential Radar uses to poll; it is never sent in a URL.
func (c *ConnectClient) Create(ctx context.Context, meta ConnectMetadata) (*CreateResponse, error) {
	if meta.DeploymentMode != "in-cluster" {
		return nil, fmt.Errorf("unsupported Cloud Connect deployment mode %q: only in-cluster agents are currently accepted", meta.DeploymentMode)
	}
	if utf8.RuneCountInString(strings.TrimSpace(meta.ClusterName)) > 80 {
		return nil, errors.New("cluster name must be at most 80 characters; choose a shorter value with --name")
	}
	if err := c.validateHubOrigin(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.HubBase+"/api/connect/requests", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching %s: %w", c.HubBase, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, newConnectHTTPError("create connect request", resp)
	}
	var out CreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding hub response: %w", err)
	}
	if out.RequestID == "" || out.DeviceSecret == "" || out.ConnectURL == "" || out.WSSURL == "" || out.ExpiresIn <= 0 || out.TokenPickupExpiresIn <= 0 || out.PollInterval <= 0 {
		return nil, errors.New("hub response missing required fields")
	}
	if err := ValidateWebSocketURL(out.WSSURL); err != nil {
		return nil, fmt.Errorf("hub response contains invalid wss_url: %w", err)
	}
	recoveryWindow, err := connectRecoveryWindow(&out)
	if err != nil {
		return nil, err
	}
	out.recoveryDeadline = time.Now().Add(recoveryWindow)
	return &out, nil
}

// Poll checks the status of a connect request, authenticating with the
// device_secret. It preserves bounded error bodies and retry metadata so the
// shared poll loop can distinguish transient responses from terminal 4xxs.
func (c *ConnectClient) Poll(ctx context.Context, requestID, deviceSecret string) (*PollResponse, error) {
	if err := c.validateHubOrigin(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.HubBase+"/api/connect/requests/"+requestID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+deviceSecret)
	resp, err := c.do(req)
	if err != nil {
		return nil, &pollTransportError{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, newConnectHTTPError("poll", resp, deviceSecret)
	}
	var out PollResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding poll response: %w", err)
	}
	if out.Status == "approved" {
		var missing []string
		if out.ClusterID == "" {
			missing = append(missing, "cluster_id")
		}
		if out.Token == "" {
			missing = append(missing, "token")
		}
		if out.WSSURL == "" {
			missing = append(missing, "wss_url")
		}
		if len(missing) > 0 {
			cluster := "a pending cluster"
			if out.ClusterID != "" {
				cluster = fmt.Sprintf("cluster %q", out.ClusterID)
			}
			return nil, fmt.Errorf("hub approved the connection but returned incomplete details (missing %s); %s may already exist in the Hub, so inspect it before starting a new install", strings.Join(missing, ", "), cluster)
		}
		if err := ValidateWebSocketURL(out.WSSURL); err != nil {
			return nil, fmt.Errorf("hub approved the connection but returned an invalid wss_url; cluster %q may already exist in the Hub, so inspect it before starting a new install: %w", out.ClusterID, err)
		}
	}
	return &out, nil
}

func newConnectHTTPError(operation string, resp *http.Response, secrets ...string) error {
	readLimit := maxConnectErrorBodyBytes + utf8.UTFMax
	for _, secret := range secrets {
		if len(secret) > readLimit-maxConnectErrorBodyBytes {
			readLimit = maxConnectErrorBodyBytes + len(secret)
		}
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(readLimit+1)))
	sourceTruncated := len(body) > readLimit
	if sourceTruncated {
		body = body[:readLimit]
	}

	// A non-EOF read failure can truncate the body in the middle of a secret just
	// as surely as the explicit size cap can. Treat either case as incomplete so
	// redactConnectSecrets also removes a matching secret prefix at the suffix.
	text := redactConnectSecrets(string(body), sourceTruncated || readErr != nil, secrets)
	if readErr != nil {
		if text != "" {
			text += "; "
		}
		readErrorText := redactConnectSecrets(readErr.Error(), true, secrets)
		text += "reading error response: " + readErrorText
	}
	text = sanitizeConnectErrorText(text)
	text, outputTruncated := truncateConnectErrorText(text)
	if sourceTruncated || readErr != nil || outputTruncated {
		text += "…"
	}
	retryAfter, hasRetryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	return &connectHTTPError{
		operation:     operation,
		statusCode:    resp.StatusCode,
		status:        sanitizeConnectErrorText(redactConnectSecrets(resp.Status, false, secrets)),
		body:          text,
		retryAfter:    retryAfter,
		hasRetryAfter: hasRetryAfter,
	}
}

func redactConnectSecrets(text string, sourceTruncated bool, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, connectErrorRedaction)
		}
	}
	if !sourceTruncated {
		return text
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		maxPrefix := min(len(secret)-1, len(text))
		for prefixLen := maxPrefix; prefixLen > 0; prefixLen-- {
			if strings.HasSuffix(text, secret[:prefixLen]) {
				text = text[:len(text)-prefixLen] + connectErrorRedaction
				break
			}
		}
	}
	return text
}

func sanitizeConnectErrorText(text string) string {
	text = strings.ToValidUTF8(text, "�")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, text)
	return strings.TrimSpace(text)
}

func truncateConnectErrorText(text string) (string, bool) {
	if len(text) <= maxConnectErrorBodyBytes {
		return text, false
	}
	cut := maxConnectErrorBodyBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

func parseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
		maxSeconds := int64((time.Duration(1<<63 - 1)) / time.Second)
		if seconds > maxSeconds {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	if !when.After(now) {
		return 0, true
	}
	return when.Sub(now), true
}

func connectRecoveryWindow(cr *CreateResponse) (time.Duration, error) {
	if cr == nil || cr.ExpiresIn <= 0 || cr.TokenPickupExpiresIn <= 0 {
		return 0, errors.New("hub response missing required expiry fields")
	}
	maxSeconds := int64((time.Duration(1<<63 - 1)) / time.Second)
	expiresIn := int64(cr.ExpiresIn)
	pickupExpiresIn := int64(cr.TokenPickupExpiresIn)
	if expiresIn > maxSeconds || pickupExpiresIn > maxSeconds-expiresIn {
		return 0, errors.New("hub response contains invalid expiry fields")
	}
	return time.Duration(expiresIn+pickupExpiresIn) * time.Second, nil
}

func connectPickupWindow(cr *CreateResponse) (time.Duration, error) {
	if cr == nil || cr.TokenPickupExpiresIn <= 0 {
		return 0, errors.New("hub response missing required token pickup expiry")
	}
	maxSeconds := int64((time.Duration(1<<63 - 1)) / time.Second)
	pickupExpiresIn := int64(cr.TokenPickupExpiresIn)
	if pickupExpiresIn > maxSeconds {
		return 0, errors.New("hub response contains invalid token pickup expiry")
	}
	return time.Duration(pickupExpiresIn) * time.Second, nil
}

func connectPollInterval(cr *CreateResponse) (time.Duration, error) {
	if cr == nil || cr.RequestID == "" || cr.DeviceSecret == "" || cr.PollInterval <= 0 {
		return 0, errors.New("hub response missing required polling fields")
	}
	if cr.PollInterval < 2 {
		return 2 * time.Second, nil
	}
	if cr.PollInterval > 30 {
		return 30 * time.Second, nil
	}
	return time.Duration(cr.PollInterval) * time.Second, nil
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

var ErrConnectPickupExpired = errors.New("connect token pickup window expired after approval")

var ErrConnectRejected = errors.New("connect request was rejected")

var ErrConnectRecoveryTimeout = errors.New("timed out recovering connect request status")

var ErrConnectConsumptionTimeout = errors.New("timed out waiting for the cloud agent to connect")

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
	return &FlowResult{
		ClusterID:   pr.ClusterID,
		Token:       pr.Token,
		WSSURL:      pr.WSSURL,
		ClusterName: meta.ClusterName,
	}, nil
}

// PollUntilApproved polls the connect request until it reaches a terminal state,
// honoring the hub-advertised poll interval (clamped to a sane 2–30s band) and
// the full approval-plus-token-pickup recovery window. It is the single poll
// loop shared by RunFlow and the in-cluster install driver so their interval,
// expiry, and terminal-state semantics can't drift apart.
//
// It polls immediately (catching an approval that landed during browser-open),
// then waits between polls without ever sleeping past the deadline. Returns the
// approved PollResponse, ErrConnectExpired when the Hub explicitly reports
// expiry, or an error on a consumed request, a terminal 4xx response, or
// context cancellation. Transport errors, 429s, and 5xx responses retry with
// bounded backoff; exhausting the local recovery window is reported as
// ambiguous because approval may have committed while the Hub was unreachable.
func (c *ConnectClient) PollUntilApproved(ctx context.Context, cr *CreateResponse) (*PollResponse, error) {
	interval, err := connectPollInterval(cr)
	if err != nil {
		return nil, err
	}
	recoveryWindow, err := connectRecoveryWindow(cr)
	if err != nil {
		return nil, err
	}
	deadline := cr.recoveryDeadline
	if deadline.IsZero() {
		deadline = time.Now().Add(recoveryWindow)
	}
	pollCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	initialRetryBackoff := c.pollRetryInitialBackoff
	if initialRetryBackoff <= 0 {
		initialRetryBackoff = time.Second
	}
	retryBackoff := initialRetryBackoff
	const maxRetryBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return c.finalPollAfterCancellation(ctx, cr, deadline)
		}
		if time.Until(deadline) <= 0 {
			return nil, connectRecoveryTimeoutError()
		}

		pr, err := c.Poll(pollCtx, cr.RequestID, cr.DeviceSecret)
		if err != nil {
			if ctx.Err() != nil {
				return c.finalPollAfterCancellation(ctx, cr, deadline)
			}
			if time.Until(deadline) <= 0 || errors.Is(pollCtx.Err(), context.DeadlineExceeded) {
				return nil, connectRecoveryTimeoutError()
			}

			wait, retry := pollRetryDelay(err, retryBackoff)
			if !retry {
				return nil, err
			}
			remaining := time.Until(deadline)
			if wait > remaining {
				wait = remaining
			}
			if !sleep(pollCtx, wait) {
				if ctx.Err() != nil {
					return c.finalPollAfterCancellation(ctx, cr, deadline)
				}
				return nil, connectRecoveryTimeoutError()
			}
			retryBackoff = nextBackoff(retryBackoff, maxRetryBackoff)
			continue
		}
		retryBackoff = initialRetryBackoff
		switch pr.Status {
		case "approved":
			return pr, nil
		case "consumed":
			// The token was already delivered + the cluster connected on a prior
			// run. A fresh flow can't retrieve it; the user re-runs.
			return nil, errors.New("this connect request was already used — start a new Cloud installation")
		case "expired":
			return nil, ErrConnectExpired
		case "pickup_expired":
			return nil, connectPickupExpiredError(pr)
		case "rejected":
			return nil, ErrConnectRejected
		case "pending":
			// Continue below after the advertised poll interval.
		default:
			return nil, fmt.Errorf("hub returned unknown connect status %q", pr.Status)
		}
		// pending — wait, but never sleep past the recovery deadline.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, connectRecoveryTimeoutError()
		}
		wait := interval
		if remaining < wait {
			wait = remaining
		}
		if !sleep(pollCtx, wait) {
			if ctx.Err() != nil {
				return c.finalPollAfterCancellation(ctx, cr, deadline)
			}
			return nil, connectRecoveryTimeoutError()
		}
	}
}

func (c *ConnectClient) finalPollAfterCancellation(ctx context.Context, cr *CreateResponse, recoveryDeadline time.Time) (*PollResponse, error) {
	remaining := time.Until(recoveryDeadline)
	if remaining <= 0 {
		return nil, connectCancellationError(ctx.Err())
	}
	timeout := min(connectFinalPollTimeout, remaining)
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	pr, err := c.Poll(finalCtx, cr.RequestID, cr.DeviceSecret)
	if err != nil {
		return nil, connectCancellationError(ctx.Err())
	}
	switch pr.Status {
	case "approved":
		return pr, nil
	case "consumed":
		return nil, errors.New("this connect request was already used — inspect the existing cluster in the Hub before starting a new Cloud installation")
	case "expired":
		return nil, ErrConnectExpired
	case "pickup_expired":
		return nil, connectPickupExpiredError(pr)
	case "rejected":
		return nil, ErrConnectRejected
	case "pending":
		return nil, connectCancellationError(ctx.Err())
	default:
		return nil, fmt.Errorf("hub returned unknown connect status %q", pr.Status)
	}
}

func connectCancellationError(cause error) error {
	return fmt.Errorf("%w: browser approval may have created a cluster in the Hub; inspect the Hub before starting a new Cloud installation", cause)
}

func connectRecoveryTimeoutError() error {
	return fmt.Errorf("%w: browser approval may have created a cluster in the Hub; inspect the Hub before starting a new Cloud installation", ErrConnectRecoveryTimeout)
}

func connectPickupExpiredError(pr *PollResponse) error {
	if pr.ClusterID == "" {
		return errors.New("hub reported an expired token pickup without the existing cluster id")
	}
	return fmt.Errorf("%w: cluster %q already exists in the Hub without a retrievable token; delete that pending cluster before starting a fresh install", ErrConnectPickupExpired, pr.ClusterID)
}

// WaitUntilConsumed waits for the installed agent to authenticate with the
// token obtained through cr. maxWait is additionally capped by the Hub's token
// pickup recovery deadline.
func (c *ConnectClient) WaitUntilConsumed(ctx context.Context, cr *CreateResponse, maxWait time.Duration) error {
	if maxWait <= 0 {
		return errors.New("consumption wait must be greater than zero")
	}
	interval, err := connectPollInterval(cr)
	if err != nil {
		return err
	}
	pickupWindow, err := connectPickupWindow(cr)
	if err != nil {
		return err
	}

	now := time.Now()
	deadline := now.Add(maxWait)
	recoveryDeadline := cr.recoveryDeadline
	if recoveryDeadline.IsZero() {
		recoveryDeadline = now.Add(pickupWindow)
	}
	if recoveryDeadline.Before(deadline) {
		deadline = recoveryDeadline
	}
	if !deadline.After(now) {
		return ErrConnectConsumptionTimeout
	}
	pollCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	initialRetryBackoff := c.pollRetryInitialBackoff
	if initialRetryBackoff <= 0 {
		initialRetryBackoff = time.Second
	}
	retryBackoff := initialRetryBackoff
	const maxRetryBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Until(deadline) <= 0 {
			return ErrConnectConsumptionTimeout
		}

		pr, err := c.Poll(pollCtx, cr.RequestID, cr.DeviceSecret)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if time.Until(deadline) <= 0 || errors.Is(pollCtx.Err(), context.DeadlineExceeded) {
				return ErrConnectConsumptionTimeout
			}
			wait, retry := pollRetryDelay(err, retryBackoff)
			if !retry {
				return err
			}
			wait = min(wait, time.Until(deadline))
			if !sleep(pollCtx, wait) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return ErrConnectConsumptionTimeout
			}
			retryBackoff = nextBackoff(retryBackoff, maxRetryBackoff)
			continue
		}

		retryBackoff = initialRetryBackoff
		switch pr.Status {
		case "consumed":
			return nil
		case "approved":
			// The token remains retrievable until the tunnel authenticates.
		case "pending":
			return errors.New("hub returned pending after the connect request was approved")
		case "expired":
			return ErrConnectExpired
		case "pickup_expired":
			return connectPickupExpiredError(pr)
		case "rejected":
			return ErrConnectRejected
		default:
			return fmt.Errorf("hub returned unknown connect status %q", pr.Status)
		}

		wait := min(interval, time.Until(deadline))
		if wait <= 0 || !sleep(pollCtx, wait) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrConnectConsumptionTimeout
		}
	}
}

func pollRetryDelay(err error, fallback time.Duration) (time.Duration, bool) {
	var transportErr *pollTransportError
	if errors.As(err, &transportErr) {
		return fallback, true
	}
	var statusErr *connectHTTPError
	if !errors.As(err, &statusErr) {
		return 0, false
	}
	if statusErr.statusCode != http.StatusTooManyRequests && (statusErr.statusCode < 500 || statusErr.statusCode > 599) {
		return 0, false
	}
	if statusErr.hasRetryAfter {
		// Retry-After is a server-requested minimum, not permission to
		// busy-loop. A zero or past-date hint still respects local backoff.
		return max(statusErr.retryAfter, fallback), true
	}
	return fallback, true
}
