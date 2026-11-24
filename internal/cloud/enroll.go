package cloud

import (
	"bytes"
	"context"
	"crypto/x509"
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
	Name         string `json:"name"`
	NameSource   string `json:"name_source"`
	RadarVersion string `json:"radar_version,omitempty"`
	K8sVersion   string `json:"k8s_version,omitempty"`
	Distro       string `json:"distro,omitempty"`
	NodeCount    *int   `json:"node_count,omitempty"`
	APIServerURL string `json:"api_server_url,omitempty"`
}

// EnrollResult is a successful join: the cluster's token for this run and
// where to open its tunnel.
type EnrollResult struct {
	ClusterID        string
	Token            string
	AgentURL         string
	OrganizationID   string
	OrganizationName string
}

type enrollResponse struct {
	ClusterID        string `json:"cluster_id"`
	Token            string `json:"token"`
	AgentURL         string `json:"agent_url"`
	Error            string `json:"error"`
	Message          string `json:"message"`
	OrganizationID   string `json:"org_id"`
	OrganizationName string `json:"org_name"`
}

// EnrollError is the Hub answering an enroll request with anything but a join.
type EnrollError struct {
	StatusCode int
	// Code is the Hub's machine-readable error, e.g. "cluster_removed".
	Code string
	// Message is the Hub's one-sentence explanation for the log, sanitized.
	Message       string
	RetryAfter    time.Duration
	HasRetryAfter bool
}

func (e *EnrollError) Error() string {
	msg := fmt.Sprintf("enroll: Hub returned %d", e.StatusCode)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// EnrollClient calls a Hub's enroll endpoint with the key in KeyFile.
type EnrollClient struct {
	HubOrigin      string
	KeyFile        string
	PrivateKeyFile string
	Organization   string
	HTTP           *http.Client
}

// NewEnrollClient builds a client for hubOrigin. A nil ca means the system
// roots.
func NewEnrollClient(hubOrigin, keyFile string, ca *x509.CertPool) (*EnrollClient, error) {
	origin, err := NormalizeHubOrigin(hubOrigin)
	if err != nil {
		return nil, err
	}
	c := &EnrollClient{
		HubOrigin: origin,
		KeyFile:   keyFile,
		HTTP:      &http.Client{Timeout: enrollRequestTimeout},
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = hubTLSConfig(ca, false)
	c.HTTP.Transport = transport
	return c, nil
}

// ReadEnrollmentKey reads and checks the key file. It runs before every
// enroll call.
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

// Enroll asks the Hub to join this cluster. The Hub answers a known cluster
// with its existing entry and a new token, so a retry after a lost response
// lands on the same entry.
func (c *EnrollClient) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(body) > maxEnrollBodyBytes {
		return nil, errors.New("enrollment request exceeds 8 KiB")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.HubOrigin+enrollPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var authorization string
	if c.PrivateKeyFile != "" {
		authorization, err = c.signedAuthorization(ctx, body)
	} else {
		var key string
		key, err = ReadEnrollmentKey(c.KeyFile)
		authorization = "Bearer " + key
	}
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", authorization)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.noRedirectClient().Do(httpReq)
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
			e.Code = truncateRunes(sanitizeConnectErrorText(parsed.Error), 64)
			e.Message = truncateRunes(sanitizeConnectErrorText(parsed.Message), maxEnrollMessageRunes)
		}
		if c.PrivateKeyFile != "" && resp.StatusCode == http.StatusUnauthorized && (e.Code == "" || e.Code == "invalid_proof" || e.Code == "key_unknown") {
			e.Code = "invalid_proof"
			e.Message = "Hub rejected this enrollment proof. Check the registered public key, organization and Hub rollout."
		}
		return nil, e
	}
	if jsonErr != nil {
		return nil, fmt.Errorf("enroll: decode Hub response: %w", jsonErr)
	}
	if parsed.ClusterID == "" || parsed.Token == "" || parsed.AgentURL == "" {
		return nil, errors.New("enroll: Hub response is missing cluster_id, token or agent_url")
	}
	if err := ValidateWebSocketURL(parsed.AgentURL); err != nil {
		return nil, fmt.Errorf("enroll: Hub returned an unusable agent_url: %w", err)
	}
	if c.PrivateKeyFile != "" {
		if ValidateEnrollmentOrganization(parsed.OrganizationID) != nil || (c.Organization != "self-hosted" && parsed.OrganizationID != c.Organization) {
			return nil, errors.New("Hub enrollment response does not match the configured organization")
		}
	}
	return &EnrollResult{ClusterID: parsed.ClusterID, Token: parsed.Token, AgentURL: parsed.AgentURL,
		OrganizationID: parsed.OrganizationID, OrganizationName: truncateRunes(sanitizeConnectErrorText(parsed.OrganizationName), 80)}, nil
}

func (c *EnrollClient) noRedirectClient() *http.Client {
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("Hub API redirects are not allowed")
	}
	return &client
}

// maxEnrollMessageRunes bounds a Hub message kept for logs.
const maxEnrollMessageRunes = 300

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
