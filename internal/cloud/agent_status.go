package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxAgentStatusBodyBytes = 8 << 10

var (
	ErrAgentStatusUnauthorized     = errors.New("Hub rejected the cluster token")
	ErrAgentStatusEndpointNotFound = errors.New("Hub agent status endpoint was not found")
)

// AgentStatusResponse is the Hub's live view of one cluster's tunnel.
// LastConnectedAt is the start of the current connection when connected and
// the start of the most recent connection when disconnected.
type AgentStatusResponse struct {
	ClusterID       string     `json:"cluster_id"`
	Status          string     `json:"status"`
	LastConnectedAt *time.Time `json:"last_connected_at,omitempty"`
}

type hubPublicConfig struct {
	FrontendURL string `json:"frontend_url"`
}

// AgentStatusClient reuses the Hub client's validation and no-redirect
// transport policy while targeting the cluster-token status endpoint.
type AgentStatusClient struct {
	*ConnectClient
}

func NewAgentStatusClient(cloudURL string) (*AgentStatusClient, error) {
	hubOrigin, err := HubOriginFromWebSocketURL(cloudURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Hub agent URL: %w", err)
	}
	c := NewConnectClient(hubOrigin)
	c.HTTP.Timeout = 5 * time.Second
	return &AgentStatusClient{ConnectClient: c}, nil
}

func (c *AgentStatusClient) Get(ctx context.Context, token string) (*AgentStatusResponse, error) {
	if token == "" {
		return nil, errors.New("cluster token is required")
	}
	if err := c.validateHubOrigin(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.HubBase+"/api/agent/status", nil)
	if err != nil {
		return nil, fmt.Errorf("build agent status request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("query Hub agent status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrAgentStatusUnauthorized
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrAgentStatusEndpointNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query Hub agent status: Hub returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAgentStatusBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Hub agent status: %w", err)
	}
	if len(body) > maxAgentStatusBodyBytes {
		return nil, errors.New("read Hub agent status: response is too large")
	}
	var status AgentStatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("decode Hub agent status: %w", err)
	}
	if status.ClusterID == "" || status.Status == "" {
		return nil, errors.New("decode Hub agent status: cluster_id and status are required")
	}
	return &status, nil
}

// GetFrontendURL returns the Hub's canonical browser origin from its public
// runtime configuration. It is intentionally independent of the cluster token
// so status can still print an actionable link when that token was rejected.
func (c *AgentStatusClient) GetFrontendURL(ctx context.Context) (string, error) {
	if err := c.validateHubOrigin(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.HubBase+"/api/config", nil)
	if err != nil {
		return "", fmt.Errorf("build Hub config request: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return "", fmt.Errorf("query Hub config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("query Hub config: Hub returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAgentStatusBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read Hub config: %w", err)
	}
	if len(body) > maxAgentStatusBodyBytes {
		return "", errors.New("read Hub config: response is too large")
	}
	var cfg hubPublicConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", fmt.Errorf("decode Hub config: %w", err)
	}
	cfg.FrontendURL = strings.TrimRight(strings.TrimSpace(cfg.FrontendURL), "/")
	if cfg.FrontendURL == "" {
		return "", errors.New("decode Hub config: frontend_url is required")
	}
	if err := ValidateHubOrigin(cfg.FrontendURL); err != nil {
		return "", fmt.Errorf("decode Hub config: invalid frontend_url: %w", err)
	}
	return cfg.FrontendURL, nil
}
