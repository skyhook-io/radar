package cloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	enrollPath           = "/api/agent/enroll"
	maxEnrollBodyBytes   = 8 << 10
	enrollmentKeyPrefix  = "rhe_"
	enrollRequestTimeout = 30 * time.Second
)

// EnrollRequest is the body of POST /api/agent/enroll.
type EnrollRequest struct {
	AgentUID     string `json:"agent_uid"`
	Namespace    string `json:"namespace"`
	Release      string `json:"release"`
	Nonce        string `json:"nonce"`
	Name         string `json:"name"`
	NameSource   string `json:"name_source"`
	RadarVersion string `json:"radar_version,omitempty"`
	K8sVersion   string `json:"k8s_version,omitempty"`
	Distro       string `json:"distro,omitempty"`
	NodeCount    *int   `json:"node_count,omitempty"`
	APIServerURL string `json:"api_server_url,omitempty"`
}

// EnrollResult is a successful join: the cluster's own token and where to
// open its tunnel.
type EnrollResult struct {
	ClusterID string
	Token     string
	WSSURL    string
}

type enrollResponse struct {
	Status    string `json:"status"`
	ClusterID string `json:"cluster_id"`
	Token     string `json:"token"`
	WSSURL    string `json:"wss_url"`
	Error     string `json:"error"`
	Detail    string `json:"detail"`
}

// EnrollError is the Hub answering an enroll request with anything but a join.
type EnrollError struct {
	StatusCode int
	// Code is the Hub's machine-readable error, e.g. "enrollment_closed".
	Code string
	// Detail is the Hub's detail field, sanitized for logs.
	Detail        string
	RetryAfter    time.Duration
	HasRetryAfter bool
}

func (e *EnrollError) Error() string {
	msg := fmt.Sprintf("enroll: Hub returned %d", e.StatusCode)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	return msg
}

// EnrollClient calls a Hub's enroll endpoint with the key in KeyFile.
type EnrollClient struct {
	HubOrigin string
	KeyFile   string
	HTTP      *http.Client
	// CA, when set, adds a CA bundle to the system roots. It is re-read for
	// every call, so a rotated bundle needs no restart.
	CA *CAFile

	caPool *x509.CertPool
}

// NewEnrollClient builds a client for hubOrigin. A nil ca means the system
// roots.
func NewEnrollClient(hubOrigin, keyFile string, ca *CAFile) (*EnrollClient, error) {
	origin, err := NormalizeHubOrigin(hubOrigin)
	if err != nil {
		return nil, err
	}
	c := &EnrollClient{
		HubOrigin: origin,
		KeyFile:   keyFile,
		HTTP:      &http.Client{Timeout: enrollRequestTimeout},
		CA:        ca,
	}
	c.useRoots(ca.Pool())
	return c, nil
}

func (c *EnrollClient) useRoots(pool *x509.CertPool) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = hubTLSConfig(pool, false)
	c.HTTP.Transport = transport
	c.caPool = pool
}

// ReadEnrollmentKey reads and checks the key file. It runs before every
// enroll call, so a key rotated in its Secret is picked up without a restart.
func ReadEnrollmentKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read enrollment key file: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", errors.New("enrollment key file is empty")
	}
	if !strings.HasPrefix(key, enrollmentKeyPrefix) || strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return "", errors.New("enrollment key file does not hold an rhe_ key")
	}
	return key, nil
}

// NewEnrollNonce returns 32 random bytes as unpadded base64url (43 chars).
func NewEnrollNonce() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Enroll asks the Hub to join this cluster. Retrying with the same nonce is
// safe: while the attempt is unclaimed the Hub answers with the same token.
func (c *EnrollClient) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResult, error) {
	key, err := ReadEnrollmentKey(c.KeyFile)
	if err != nil {
		return nil, err
	}
	if c.CA != nil {
		if pool := c.CA.Pool(); pool != c.caPool {
			c.useRoots(pool)
		}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.HubOrigin+enrollPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")

	// Never follow a redirect: it could carry the key somewhere else,
	// including to plain HTTP.
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("Hub API redirects are not allowed")
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, &HubUnreachableError{HubBase: c.HubOrigin, Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxEnrollBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("enroll: read Hub response: %w", err)
	}
	if len(raw) > maxEnrollBodyBytes {
		raw = raw[:maxEnrollBodyBytes]
	}
	var parsed enrollResponse
	jsonErr := json.Unmarshal(raw, &parsed)

	if resp.StatusCode != http.StatusOK {
		retryAfter, hasRetryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		e := &EnrollError{StatusCode: resp.StatusCode, RetryAfter: retryAfter, HasRetryAfter: hasRetryAfter}
		if jsonErr == nil {
			e.Code = sanitizeShort(parsed.Error)
			e.Detail = sanitizeShort(parsed.Detail)
		}
		return nil, e
	}
	if jsonErr != nil {
		return nil, fmt.Errorf("enroll: decode Hub response: %w", jsonErr)
	}
	if parsed.Status != "joined" || parsed.ClusterID == "" || parsed.Token == "" || parsed.WSSURL == "" {
		return nil, errors.New("enroll: Hub response is missing status, cluster_id, token or wss_url")
	}
	if err := ValidateWebSocketURL(parsed.WSSURL); err != nil {
		return nil, fmt.Errorf("enroll: Hub returned an unusable wss_url: %w", err)
	}
	return &EnrollResult{ClusterID: parsed.ClusterID, Token: parsed.Token, WSSURL: parsed.WSSURL}, nil
}

func sanitizeShort(s string) string {
	s = sanitizeConnectErrorText(s)
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120])
	}
	return s
}
