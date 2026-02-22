package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Client is a Prometheus HTTP API client with auto-discovery.
type Client struct {
	mu sync.RWMutex

	// Discovered/configured connection
	baseURL  string // e.g. "http://localhost:54321" or "http://prometheus.monitoring.svc:9090"
	basePath string // e.g. "/select/0/prometheus" for vmselect

	// Discovery state
	discovered       bool
	discoveryService *ServiceInfo // discovered service info for port-forward
	manualURL        string       // --prometheus-url override

	// K8s clients for discovery
	k8sClient   kubernetes.Interface
	k8sConfig   *rest.Config
	contextName string

	httpClient *http.Client
}

// ServiceInfo holds info about a discovered Prometheus service.
type ServiceInfo struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      int    `json:"port"`
	BasePath  string `json:"basePath,omitempty"`
}

// Status represents the current Prometheus connection status.
type Status struct {
	Available   bool         `json:"available"`
	Connected   bool         `json:"connected"`
	Address     string       `json:"address,omitempty"`
	Service     *ServiceInfo `json:"service,omitempty"`
	ContextName string       `json:"contextName,omitempty"`
	Error       string       `json:"error,omitempty"`
}

// Global client instance
var (
	globalClient *Client
	clientMu     sync.RWMutex
)

// Initialize creates the global Prometheus client.
func Initialize(client kubernetes.Interface, config *rest.Config, contextName string) {
	clientMu.Lock()
	defer clientMu.Unlock()

	globalClient = &Client{
		k8sClient:   client,
		k8sConfig:   config,
		contextName: contextName,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SetManualURL sets the --prometheus-url override on the global client.
func SetManualURL(rawURL string) {
	clientMu.Lock()
	defer clientMu.Unlock()
	if globalClient != nil {
		globalClient.manualURL = strings.TrimRight(rawURL, "/")
	}
}

// SetURL overrides discovery with a specific Prometheus URL.
// Clears existing connection state so the next EnsureConnected uses this URL.
func (c *Client) SetURL(rawURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.manualURL = strings.TrimRight(rawURL, "/")
	c.baseURL = ""
	c.basePath = ""
	c.discovered = false
	c.discoveryService = nil
}

// GetClient returns the global Prometheus client (may be nil).
func GetClient() *Client {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return globalClient
}

// Reset clears connection state so the next query triggers rediscovery (used on context switch).
func Reset() {
	clientMu.Lock()
	defer clientMu.Unlock()
	if globalClient != nil {
		globalClient.mu.Lock()
		globalClient.baseURL = ""
		globalClient.basePath = ""
		globalClient.discovered = false
		globalClient.discoveryService = nil
		globalClient.mu.Unlock()
	}
}

// Reinitialize recreates the client with new K8s connection info.
func Reinitialize(client kubernetes.Interface, config *rest.Config, contextName string) {
	clientMu.Lock()
	defer clientMu.Unlock()

	manualURL := ""
	if globalClient != nil {
		manualURL = globalClient.manualURL
	}

	globalClient = &Client{
		k8sClient:   client,
		k8sConfig:   config,
		contextName: contextName,
		manualURL:   manualURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetStatus returns the current Prometheus connection status.
func (c *Client) GetStatus() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var svc *ServiceInfo
	if c.discoveryService != nil {
		cp := *c.discoveryService
		svc = &cp
	}

	return Status{
		Available:   c.baseURL != "",
		Connected:   c.baseURL != "",
		Address:     c.baseURL,
		Service:     svc,
		ContextName: c.contextName,
	}
}

// EnsureConnected attempts to discover and connect to Prometheus if not already connected.
// Returns the base URL and base path, or an error.
func (c *Client) EnsureConnected(ctx context.Context) (string, string, error) {
	c.mu.RLock()
	if c.baseURL != "" {
		// Verify cached address still works
		base := c.baseURL
		bp := c.basePath
		c.mu.RUnlock()
		if c.probe(ctx, base+bp) {
			return base, bp, nil
		}
		// Stale — clear and rediscover
		c.mu.Lock()
		c.baseURL = ""
		c.basePath = ""
		c.discovered = false
		c.mu.Unlock()
	} else {
		c.mu.RUnlock()
	}

	return c.discover(ctx)
}

// QueryRange executes a Prometheus range query.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*QueryResult, error) {
	base, basePath, err := c.EnsureConnected(ctx)
	if err != nil {
		return nil, err
	}

	params := url.Values{
		"query": {query},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {fmt.Sprintf("%.0f", step.Seconds())},
	}

	reqURL := fmt.Sprintf("%s%s/api/v1/query_range?%s", base, basePath, params.Encode())
	return c.doQuery(ctx, reqURL)
}

// Query executes a Prometheus instant query.
func (c *Client) Query(ctx context.Context, query string) (*QueryResult, error) {
	base, basePath, err := c.EnsureConnected(ctx)
	if err != nil {
		return nil, err
	}

	params := url.Values{
		"query": {query},
	}

	reqURL := fmt.Sprintf("%s%s/api/v1/query?%s", base, basePath, params.Encode())
	return c.doQuery(ctx, reqURL)
}

func (c *Client) doQuery(ctx context.Context, reqURL string) (*QueryResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying prometheus: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB cap
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned status %d: %s", resp.StatusCode, string(body))
	}

	var promResp promResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s (%s)", promResp.Error, promResp.ErrorType)
	}

	return parseQueryResult(promResp.Data)
}

// probe checks if a Prometheus endpoint is reachable.
func (c *Client) probe(ctx context.Context, addr string) bool {
	testCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(testCtx, "GET", addr+"/api/v1/query?query=up", nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// Prometheus API response types

type promResponse struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	ErrorType string          `json:"errorType,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// QueryResult is the parsed result of a Prometheus query.
type QueryResult struct {
	ResultType string   `json:"resultType"`
	Series     []Series `json:"series"`
}

// Series is a single time series from a Prometheus query.
type Series struct {
	Labels     map[string]string `json:"labels"`
	DataPoints []DataPoint       `json:"dataPoints"`
}

// DataPoint is a single (timestamp, value) pair.
type DataPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

func parseQueryResult(data json.RawMessage) (*QueryResult, error) {
	var raw struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]interface{}   `json:"values"` // for matrix
			Value  []interface{}     `json:"value"`  // for vector
		} `json:"result"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing result: %w", err)
	}

	result := &QueryResult{
		ResultType: raw.ResultType,
		Series:     make([]Series, 0, len(raw.Result)),
	}

	for _, r := range raw.Result {
		series := Series{
			Labels: r.Metric,
		}

		if raw.ResultType == "matrix" {
			series.DataPoints = make([]DataPoint, 0, len(r.Values))
			for _, v := range r.Values {
				dp, err := parseDataPoint(v)
				if err != nil {
					log.Printf("[prometheus] Skipping invalid data point: %v", err)
					continue
				}
				series.DataPoints = append(series.DataPoints, dp)
			}
		} else if raw.ResultType == "vector" && r.Value != nil {
			dp, err := parseDataPoint(r.Value)
			if err != nil {
				log.Printf("[prometheus] Skipping invalid vector data point: %v", err)
			} else {
				series.DataPoints = []DataPoint{dp}
			}
		}

		result.Series = append(result.Series, series)
	}

	return result, nil
}

func parseDataPoint(v []interface{}) (DataPoint, error) {
	if len(v) != 2 {
		return DataPoint{}, fmt.Errorf("expected 2 elements, got %d", len(v))
	}

	// Timestamp can be float64 or json.Number
	var ts float64
	switch t := v[0].(type) {
	case float64:
		ts = t
	case json.Number:
		var err error
		ts, err = t.Float64()
		if err != nil {
			return DataPoint{}, fmt.Errorf("parsing timestamp: %w", err)
		}
	default:
		return DataPoint{}, fmt.Errorf("unexpected timestamp type: %T", v[0])
	}

	// Value is always a string in Prometheus responses
	valStr, ok := v[1].(string)
	if !ok {
		return DataPoint{}, fmt.Errorf("expected string value, got %T", v[1])
	}
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return DataPoint{}, fmt.Errorf("parsing value %q: %w", valStr, err)
	}

	return DataPoint{
		Timestamp: int64(ts),
		Value:     val,
	}, nil
}
