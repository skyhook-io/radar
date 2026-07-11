package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"
	"unicode"
	"unicode/utf8"
)

func TestConnectClient_Create(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/connect/requests" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var meta ConnectMetadata
		_ = json.NewDecoder(r.Body).Decode(&meta)
		if meta.DeploymentMode != "in-cluster" || meta.ClusterName != "prod" {
			t.Errorf("metadata not forwarded: %+v", meta)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreateResponse{
			RequestID: "req000000000000000000", DeviceSecret: "sec",
			ConnectURL: "https://app/connect/req000000000000000000", ExpiresIn: 900, TokenPickupExpiresIn: 1800, PollInterval: 5,
			WSSURL: "wss://api/agent",
		})
	}))
	defer srv.Close()

	c := NewConnectClient(srv.URL)
	cr, err := c.Create(context.Background(), ConnectMetadata{DeploymentMode: "in-cluster", ClusterName: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if cr.RequestID != "req000000000000000000" || cr.DeviceSecret != "sec" || cr.TokenPickupExpiresIn != 1800 {
		t.Errorf("bad create response: %+v", cr)
	}
}

func TestConnectClient_CreateRequiresAgentURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreateResponse{
			RequestID: "req1", DeviceSecret: "sec", ConnectURL: "https://app/connect/req1",
			ExpiresIn: 900, TokenPickupExpiresIn: 1800, PollInterval: 5,
		})
	}))
	defer srv.Close()

	_, err := NewConnectClient(srv.URL).Create(context.Background(), ConnectMetadata{DeploymentMode: "in-cluster"})
	if err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("missing wss_url must fail create, got %v", err)
	}
}

func TestConnectClient_CreateRejectsPlaintextRemoteAgentURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreateResponse{
			RequestID: "req1", DeviceSecret: "sec", ConnectURL: "https://app/connect/req1",
			ExpiresIn: 900, TokenPickupExpiresIn: 1800, PollInterval: 5,
			WSSURL: "ws://api.example/agent",
		})
	}))
	defer srv.Close()

	_, err := NewConnectClient(srv.URL).Create(context.Background(), ConnectMetadata{DeploymentMode: "in-cluster"})
	if err == nil || !strings.Contains(err.Error(), "must use wss://") {
		t.Fatalf("plaintext remote wss_url must fail create, got %v", err)
	}
}

func TestConnectClientRejectsPlaintextRemoteHubBeforeRequest(t *testing.T) {
	for _, operation := range []string{"create", "poll"} {
		t.Run(operation, func(t *testing.T) {
			requests := 0
			c := NewConnectClient("http://api.example")
			c.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("request must not be sent")
			})}

			var err error
			if operation == "create" {
				_, err = c.Create(context.Background(), ConnectMetadata{DeploymentMode: "in-cluster"})
			} else {
				_, err = c.Poll(context.Background(), "req1", "device-secret")
			}
			if err == nil || !strings.Contains(err.Error(), "must use https://") {
				t.Fatalf("%s error = %v, want TLS policy error", operation, err)
			}
			if requests != 0 {
				t.Fatalf("%s sent %d requests, want 0", operation, requests)
			}
		})
	}
}

func TestConnectClientRejectsHTTPSDowngradeRedirect(t *testing.T) {
	redirectedRequests := 0
	plaintext := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests++
	}))
	defer plaintext.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plaintext.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer secure.Close()

	c := NewConnectClient(secure.URL)
	c.HTTP = secure.Client()
	_, err := c.Poll(context.Background(), "req1", "device-secret")
	if err == nil || !strings.Contains(err.Error(), "redirects are not allowed") {
		t.Fatalf("downgrade redirect error = %v, want redirect policy error", err)
	}
	if redirectedRequests != 0 {
		t.Fatalf("plaintext redirect target received %d requests, want 0", redirectedRequests)
	}
}

func TestConnectClient_CreateRequiresTokenPickupExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreateResponse{
			RequestID: "req1", DeviceSecret: "sec", ConnectURL: "https://app/connect/req1",
			ExpiresIn: 900, PollInterval: 5, WSSURL: "wss://api/agent",
		})
	}))
	defer srv.Close()

	_, err := NewConnectClient(srv.URL).Create(context.Background(), ConnectMetadata{DeploymentMode: "in-cluster"})
	if err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("missing token_pickup_expires_in must fail create, got %v", err)
	}
}

func TestConnectClient_CreateRejectsLocalModeBeforeRequest(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	_, err := NewConnectClient(srv.URL).Create(context.Background(), ConnectMetadata{DeploymentMode: "local"})
	if err == nil || !strings.Contains(err.Error(), "only in-cluster agents") {
		t.Fatalf("want unsupported-mode error, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("hub received %d requests, want 0", requests)
	}
}

func TestConnectClient_CreateRejectsOverlongNameBeforeRequest(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	_, err := NewConnectClient(srv.URL).Create(context.Background(), ConnectMetadata{
		DeploymentMode: "in-cluster",
		ClusterName:    strings.Repeat("é", 81),
	})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("want actionable name error, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("hub received %d requests, want 0", requests)
	}
}

func TestConnectClient_Create_Non201(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "connect requests are disabled", http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := NewConnectClient(srv.URL).Create(context.Background(), ConnectMetadata{DeploymentMode: "in-cluster"})
	if err == nil {
		t.Fatal("expected error on non-201")
	}
	if !strings.Contains(err.Error(), "403 Forbidden") || !strings.Contains(err.Error(), "connect requests are disabled") {
		t.Fatalf("error did not preserve status and response body: %v", err)
	}
}

func TestConnectClient_Poll_SendsBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer my-secret" {
			t.Errorf("Authorization = %q, want Bearer my-secret", got)
		}
		_ = json.NewEncoder(w).Encode(PollResponse{Status: "pending"})
	}))
	defer srv.Close()
	pr, err := NewConnectClient(srv.URL).Poll(context.Background(), "req1", "my-secret")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Status != "pending" {
		t.Errorf("status = %q", pr.Status)
	}
}

func TestConnectClient_Poll_401IsTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized for bad-device-secret", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := NewConnectClient(srv.URL).Poll(context.Background(), "req1", "bad-device-secret")
	if err == nil {
		t.Fatal("401 must be a terminal error")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("error did not preserve response body: %v", err)
	}
	if strings.Contains(err.Error(), "bad-device-secret") {
		t.Fatalf("error leaked the device secret: %v", err)
	}
}

func TestConnectHTTPErrorRedactsBeforeTruncating(t *testing.T) {
	secret := "device-secret-crossing-the-error-boundary"
	prefix := strings.Repeat("x", maxConnectErrorBodyBytes-len(connectErrorRedaction))
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Status:     "500 Internal Server Error",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(prefix + secret + "tail")),
	}

	err := newConnectHTTPError("poll", resp, secret)
	got := err.Error()
	if strings.Contains(got, secret) || strings.Contains(got, secret[:12]) {
		t.Fatalf("error leaked a full or partial secret: %q", got[len(got)-100:])
	}
	if !strings.Contains(got, connectErrorRedaction) || !strings.HasSuffix(got, "…") {
		t.Fatalf("error did not preserve redaction and truncation markers: %q", got[len(got)-100:])
	}
}

func TestConnectHTTPErrorRedactsPartialSecretOnReadFailure(t *testing.T) {
	secret := "device-secret-partially-read-before-error"
	partialSecret := secret[:24]
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Header:     make(http.Header),
		Body: io.NopCloser(io.MultiReader(
			strings.NewReader("upstream echoed "+partialSecret),
			iotest.ErrReader(io.ErrUnexpectedEOF),
		)),
	}

	err := newConnectHTTPError("poll", resp, secret)
	got := err.Error()
	if strings.Contains(got, partialSecret) {
		t.Fatalf("error leaked a partial device secret: %q", got)
	}
	if !strings.Contains(got, connectErrorRedaction) || !strings.Contains(got, io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("error did not preserve redaction and read diagnostics: %q", got)
	}
}

func TestConnectHTTPErrorRedactsSecretFromStatus(t *testing.T) {
	secret := "device-secret-in-reason-phrase"
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Status:     "500 upstream echoed " + secret,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("failed")),
	}

	err := newConnectHTTPError("poll", resp, secret)
	got := err.Error()
	if strings.Contains(got, secret) {
		t.Fatalf("error leaked the device secret from the HTTP status: %q", got)
	}
	if !strings.Contains(got, "500 upstream echoed "+connectErrorRedaction) {
		t.Fatalf("error did not preserve a redacted HTTP status: %q", got)
	}
}

func TestConnectHTTPErrorBoundsReadFailureDiagnostic(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Header:     make(http.Header),
		Body:       io.NopCloser(iotest.ErrReader(errors.New(strings.Repeat("x", 2*maxConnectErrorBodyBytes)))),
	}

	err := newConnectHTTPError("poll", resp)
	var httpErr *connectHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T, want *connectHTTPError", err)
	}
	if len(httpErr.body) > maxConnectErrorBodyBytes+len("…") {
		t.Fatalf("bounded error body length = %d", len(httpErr.body))
	}
	if !strings.HasSuffix(httpErr.body, "…") {
		t.Fatalf("bounded error omitted truncation marker: %q", httpErr.body)
	}
}

func TestConnectHTTPErrorSanitizesUTF8AndControls(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string([]byte("safe\x1b[31m\nnext\x00\xff")))),
	}

	err := newConnectHTTPError("poll", resp)
	got := err.Error()
	if !utf8.ValidString(got) || !strings.Contains(got, "�") {
		t.Fatalf("error is not sanitized valid UTF-8: %q", got)
	}
	for _, r := range got {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			t.Fatalf("error retained control character %U: %q", r, got)
		}
	}
}

func TestConnectHTTPErrorTruncatesOnUTF8Boundary(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			strings.Repeat("a", maxConnectErrorBodyBytes-1) + "é" + strings.Repeat("b", 10),
		)),
	}

	err := newConnectHTTPError("poll", resp).(*connectHTTPError)
	if !utf8.ValidString(err.body) || !strings.HasSuffix(err.body, "…") {
		t.Fatalf("truncated body is not valid UTF-8 with an ellipsis: %q", err.body[len(err.body)-20:])
	}
}

func TestPollUntilApproved_IncompleteApprovalIsTerminalAndActionable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response PollResponse
		missing  []string
	}{
		{
			name:     "token",
			response: PollResponse{Status: "approved", ClusterID: "clus1", WSSURL: "wss://api/agent"},
			missing:  []string{"token"},
		},
		{
			name:     "cluster_id",
			response: PollResponse{Status: "approved", Token: "rhc_tok", WSSURL: "wss://api/agent"},
			missing:  []string{"cluster_id"},
		},
		{
			name:     "wss_url",
			response: PollResponse{Status: "approved", ClusterID: "clus1", Token: "rhc_tok"},
			missing:  []string{"wss_url"},
		},
		{
			name:     "multiple_fields",
			response: PollResponse{Status: "approved", ClusterID: "clus1"},
			missing:  []string{"token", "wss_url"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			polls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				polls++
				_ = json.NewEncoder(w).Encode(tc.response)
			}))
			defer srv.Close()

			cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 10, TokenPickupExpiresIn: 10, PollInterval: 1}
			_, err := NewConnectClient(srv.URL).PollUntilApproved(context.Background(), cr)
			if err == nil {
				t.Fatal("incomplete approved response must be terminal")
			}
			for _, want := range append([]string{"incomplete details", "inspect", "before starting a new install"}, tc.missing...) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error missing %q: %v", want, err)
				}
			}
			if polls != 1 {
				t.Fatalf("polls = %d, want 1 terminal poll", polls)
			}
		})
	}
}

func TestPollUntilApproved_InvalidAgentURLIsTerminalAndActionable(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		polls++
		_ = json.NewEncoder(w).Encode(PollResponse{
			Status: "approved", ClusterID: "clus1", Token: "rhc_tok", WSSURL: "ws://api.example/agent",
		})
	}))
	defer srv.Close()

	cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 10, TokenPickupExpiresIn: 10, PollInterval: 1}
	_, err := NewConnectClient(srv.URL).PollUntilApproved(context.Background(), cr)
	if err == nil {
		t.Fatal("invalid approved wss_url must be terminal")
	}
	for _, want := range []string{"invalid wss_url", "cluster \"clus1\"", "inspect", "before starting a new install", "must use wss://"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
	if polls != 1 {
		t.Fatalf("polls = %d, want 1", polls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRunFlow_RetriesTransientPollFailuresWithoutRecreating(t *testing.T) {
	var creates, polls int
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			creates++
			return &http.Response{
				StatusCode: http.StatusCreated,
				Status:     "201 Created",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"request_id":"req1","device_secret":"sec","connect_url":"https://app/connect/req1",
					"expires_in":10,"token_pickup_expires_in":10,"poll_interval":1,"wss_url":"wss://api/agent"
				}`)),
			}, nil
		}

		polls++
		switch polls {
		case 1:
			return nil, io.ErrUnexpectedEOF
		case 2:
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Header:     http.Header{"Retry-After": []string{"0"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"slow down"}`)),
			}, nil
		case 3:
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Header:     http.Header{"Retry-After": []string{"0"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"database unavailable"}`)),
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"status":"approved","cluster_id":"clus1","token":"rhc_tok","wss_url":"wss://api/agent"
				}`)),
			}, nil
		}
	})

	c := NewConnectClient("https://hub.example")
	c.HTTP = &http.Client{Transport: transport}
	c.pollRetryInitialBackoff = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.RunFlow(ctx, ConnectMetadata{DeploymentMode: "in-cluster", ClusterName: "prod"}, io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ClusterID != "clus1" || res.Token != "rhc_tok" {
		t.Fatalf("result = %+v", res)
	}
	if creates != 1 {
		t.Fatalf("create requests = %d, want exactly 1", creates)
	}
	if polls != 4 {
		t.Fatalf("poll requests = %d, want 4", polls)
	}
}

func TestPollUntilApproved_Other4xxIsTerminal(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"approval is not allowed"}`)
	}))
	defer srv.Close()

	cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 10, TokenPickupExpiresIn: 10, PollInterval: 1}
	_, err := NewConnectClient(srv.URL).PollUntilApproved(context.Background(), cr)
	if err == nil || !strings.Contains(err.Error(), "approval is not allowed") {
		t.Fatalf("want terminal error with response body, got %v", err)
	}
	if polls != 1 {
		t.Fatalf("polls = %d, want 1", polls)
	}
}

func TestPollUntilApproved_StopsAtDeviceDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 1, TokenPickupExpiresIn: 2, PollInterval: 1}
	started := time.Now()
	_, err := NewConnectClient(srv.URL).PollUntilApproved(context.Background(), cr)
	if !errors.Is(err, ErrConnectRecoveryTimeout) || !strings.Contains(err.Error(), "inspect the Hub") {
		t.Fatalf("want ambiguous recovery timeout with Hub guidance, got %v", err)
	}
	if elapsed := time.Since(started); elapsed < 2500*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("device deadline observed after %s, want approval TTL plus token pickup window", elapsed)
	}
}

func TestPollUntilApproved_CatchesApprovalInFinalPollGrace(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls == 1 {
			_ = json.NewEncoder(w).Encode(PollResponse{Status: "pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(PollResponse{Status: "approved", ClusterID: "clus1", Token: "rhc_tok", WSSURL: "wss://api/agent"})
	}))
	defer srv.Close()

	cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 1, TokenPickupExpiresIn: 2, PollInterval: 2}
	started := time.Now()
	resp, err := NewConnectClient(srv.URL).PollUntilApproved(context.Background(), cr)
	if err != nil || resp.Status != "approved" {
		t.Fatalf("final grace poll = %+v, %v", resp, err)
	}
	if elapsed := time.Since(started); elapsed < 1500*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("final approval arrived after %s", elapsed)
	}
}

func TestConnectRecoveryWindowIncludesApprovalAndPickupTTLs(t *testing.T) {
	window, err := connectRecoveryWindow(&CreateResponse{ExpiresIn: 15 * 60, TokenPickupExpiresIn: 30 * 60})
	if err != nil {
		t.Fatal(err)
	}
	if window != 45*time.Minute {
		t.Fatalf("recovery window = %s, want 45m", window)
	}
}

func TestPollUntilApproved_FinalPollCatchesApprovalAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls == 1 {
			cancel()
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(PollResponse{
			Status: "approved", ClusterID: "clus1", Token: "rhc_tok", WSSURL: "wss://api/agent",
		})
	}))
	defer srv.Close()

	cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 10, TokenPickupExpiresIn: 10, PollInterval: 1}
	pr, err := NewConnectClient(srv.URL).PollUntilApproved(ctx, cr)
	if err != nil || pr == nil || pr.Status != "approved" {
		t.Fatalf("final detached poll = %+v, %v", pr, err)
	}
	if polls != 2 {
		t.Fatalf("polls = %d, want canceled request plus one final poll", polls)
	}
}

func TestPollUntilApproved_CancellationWarnsWhenFinalPollIsPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls == 1 {
			cancel()
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(PollResponse{Status: "pending"})
	}))
	defer srv.Close()

	cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 10, TokenPickupExpiresIn: 10, PollInterval: 1}
	_, err := NewConnectClient(srv.URL).PollUntilApproved(ctx, cr)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "inspect the Hub") {
		t.Fatalf("cancellation guidance = %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{raw: "7", want: 7 * time.Second},
		{raw: now.Add(12 * time.Second).Format(http.TimeFormat), want: 12 * time.Second},
	} {
		t.Run(fmt.Sprintf("retry-after-%s", tc.raw), func(t *testing.T) {
			got, ok := parseRetryAfter(tc.raw, now)
			if !ok || got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = (%s, %v), want (%s, true)", tc.raw, got, ok, tc.want)
			}
		})
	}
}

func TestPollRetryDelayFloorsRetryAfterAtLocalBackoff(t *testing.T) {
	err := &connectHTTPError{
		statusCode:    http.StatusTooManyRequests,
		retryAfter:    0,
		hasRetryAfter: true,
	}
	if got, retry := pollRetryDelay(err, 2*time.Second); !retry || got != 2*time.Second {
		t.Fatalf("pollRetryDelay = (%s, %v), want (2s, true)", got, retry)
	}
}

func TestWaitUntilConsumed(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		if got := r.Header.Get("Authorization"); got != "Bearer sec" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(PollResponse{Status: "consumed"})
	}))
	defer srv.Close()

	cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 10, TokenPickupExpiresIn: 10, PollInterval: 1}
	if err := NewConnectClient(srv.URL).WaitUntilConsumed(context.Background(), cr, time.Second); err != nil {
		t.Fatal(err)
	}
	if polls != 1 {
		t.Fatalf("polls = %d, want 1", polls)
	}
}

func TestWaitUntilConsumed_RetriesTransientFailures(t *testing.T) {
	var polls int
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		polls++
		if polls == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Header:     http.Header{"Retry-After": []string{"0"}},
				Body:       io.NopCloser(strings.NewReader("temporarily unavailable")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"status":"consumed"}`)),
		}, nil
	})

	c := NewConnectClient("https://hub.example")
	c.HTTP = &http.Client{Transport: transport}
	c.pollRetryInitialBackoff = time.Millisecond
	cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 10, TokenPickupExpiresIn: 10, PollInterval: 1}
	if err := c.WaitUntilConsumed(context.Background(), cr, time.Second); err != nil {
		t.Fatal(err)
	}
	if polls != 2 {
		t.Fatalf("polls = %d, want 2", polls)
	}
}

func TestWaitUntilConsumed_UsesBoundedRecoveryDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(PollResponse{
			Status: "approved", ClusterID: "clus1", Token: "rhc_tok", WSSURL: "wss://api/agent",
		})
	}))
	defer srv.Close()

	cr := &CreateResponse{
		RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 10, TokenPickupExpiresIn: 10, PollInterval: 1,
		recoveryDeadline: time.Now().Add(50 * time.Millisecond),
	}
	started := time.Now()
	err := NewConnectClient(srv.URL).WaitUntilConsumed(context.Background(), cr, 5*time.Second)
	if !errors.Is(err, ErrConnectConsumptionTimeout) {
		t.Fatalf("want ErrConnectConsumptionTimeout, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("bounded wait took %s", elapsed)
	}
}

func TestWaitUntilConsumed_FailsClosedOnOtherStatuses(t *testing.T) {
	for _, tc := range []struct {
		status    string
		clusterID string
		wantErr   error
		wantText  string
	}{
		{status: "pending", wantText: "returned pending"},
		{status: "expired", wantErr: ErrConnectExpired},
		{status: "pickup_expired", clusterID: "clus1", wantErr: ErrConnectPickupExpired},
		{status: "rejected", wantErr: ErrConnectRejected},
		{status: "garbled", wantText: `unknown connect status "garbled"`},
	} {
		t.Run(tc.status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(PollResponse{Status: tc.status, ClusterID: tc.clusterID})
			}))
			defer srv.Close()

			cr := &CreateResponse{RequestID: "req1", DeviceSecret: "sec", ExpiresIn: 10, TokenPickupExpiresIn: 10, PollInterval: 1}
			err := NewConnectClient(srv.URL).WaitUntilConsumed(context.Background(), cr, time.Second)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantText != "" && (err == nil || !strings.Contains(err.Error(), tc.wantText)) {
				t.Fatalf("error = %v, want %q", err, tc.wantText)
			}
		})
	}
}

// TestRunFlow_PendingThenApproved drives the whole flow: the hub returns pending
// once, then approved with a token.
func TestRunFlow_PendingThenApproved(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(CreateResponse{
				RequestID: "req1", DeviceSecret: "sec",
				ConnectURL: "https://app/connect/req1", ExpiresIn: 900, TokenPickupExpiresIn: 1800, PollInterval: 5,
				WSSURL: "wss://api/agent",
			})
		default:
			polls++
			if polls == 1 {
				_ = json.NewEncoder(w).Encode(PollResponse{Status: "pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(PollResponse{Status: "approved", ClusterID: "clus1", Token: "rhc_tok", WSSURL: "wss://api/agent"})
		}
	}))
	defer srv.Close()

	c := NewConnectClient(srv.URL)
	cr, err := c.Create(context.Background(), ConnectMetadata{DeploymentMode: "in-cluster"})
	if err != nil {
		t.Fatal(err)
	}
	// First poll pending, second approved.
	if pr, _ := c.Poll(context.Background(), cr.RequestID, cr.DeviceSecret); pr.Status != "pending" {
		t.Fatalf("first poll: %+v", pr)
	}
	pr, err := c.Poll(context.Background(), cr.RequestID, cr.DeviceSecret)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Status != "approved" || pr.Token != "rhc_tok" || pr.ClusterID != "clus1" {
		t.Fatalf("second poll: %+v", pr)
	}
}

// TestRunFlow_Approved exercises RunFlow end to end with an immediate approval
// so the 5s floor only bites once; kept short with a context deadline guard.
func TestRunFlow_Approved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(CreateResponse{
				RequestID: "req1", DeviceSecret: "sec",
				ConnectURL: "https://app/connect/req1", ExpiresIn: 900, TokenPickupExpiresIn: 1800, PollInterval: 1, WSSURL: "wss://api/agent",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(PollResponse{Status: "approved", ClusterID: "clus1", Token: "rhc_tok", WSSURL: "wss://api/agent"})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var opened string
	res, err := NewConnectClient(srv.URL).RunFlow(ctx, ConnectMetadata{DeploymentMode: "in-cluster", ClusterName: "prod"}, io.Discard, func(u string) { opened = u })
	if err != nil {
		t.Fatal(err)
	}
	if res.Token != "rhc_tok" || res.ClusterID != "clus1" {
		t.Errorf("flow result: %+v", res)
	}
	if res.WSSURL != "wss://api/agent" {
		t.Errorf("wss = %q", res.WSSURL)
	}
	if !strings.Contains(opened, "/connect/req1") {
		t.Errorf("browser opened %q", opened)
	}
	if res.ClusterName != "prod" {
		t.Errorf("cluster name not carried: %q", res.ClusterName)
	}
}

func TestRunFlow_Expired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(CreateResponse{
				RequestID: "req1", DeviceSecret: "sec", ConnectURL: "https://app/connect/req1",
				ExpiresIn: 900, TokenPickupExpiresIn: 1800, PollInterval: 1, WSSURL: "wss://api/agent",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(PollResponse{Status: "expired"})
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := NewConnectClient(srv.URL).RunFlow(ctx, ConnectMetadata{DeploymentMode: "in-cluster"}, io.Discard, nil)
	if err != ErrConnectExpired {
		t.Fatalf("want ErrConnectExpired, got %v", err)
	}
}

func TestRunFlow_PickupExpiredNamesExistingCluster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(CreateResponse{
				RequestID: "req1", DeviceSecret: "sec", ConnectURL: "https://app/connect/req1",
				ExpiresIn: 900, TokenPickupExpiresIn: 1800, PollInterval: 1, WSSURL: "wss://api/agent",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(PollResponse{Status: "pickup_expired", ClusterID: "clus_existing"})
	}))
	defer srv.Close()

	_, err := NewConnectClient(srv.URL).RunFlow(context.Background(), ConnectMetadata{DeploymentMode: "in-cluster"}, io.Discard, nil)
	if !errors.Is(err, ErrConnectPickupExpired) || !strings.Contains(err.Error(), "clus_existing") || !strings.Contains(err.Error(), "delete") {
		t.Fatalf("pickup-expired guidance = %v", err)
	}
}

func TestRunFlow_RejectsTerminalAndUnknownStatuses(t *testing.T) {
	for _, tc := range []struct {
		status  string
		wantErr error
		want    string
	}{
		{status: "rejected", wantErr: ErrConnectRejected},
		{status: "garbled", want: `unknown connect status "garbled"`},
	} {
		t.Run(tc.status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(CreateResponse{
						RequestID: "req1", DeviceSecret: "sec", ConnectURL: "https://app/connect/req1",
						ExpiresIn: 900, TokenPickupExpiresIn: 1800, PollInterval: 1, WSSURL: "wss://api/agent",
					})
					return
				}
				_ = json.NewEncoder(w).Encode(PollResponse{Status: tc.status})
			}))
			defer srv.Close()

			_, err := NewConnectClient(srv.URL).RunFlow(context.Background(), ConnectMetadata{DeploymentMode: "in-cluster"}, io.Discard, nil)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
