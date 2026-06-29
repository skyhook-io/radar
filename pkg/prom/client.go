package prom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Client is a Prometheus HTTP API client that delegates all network calls to
// the injected Transport. The Client itself is stateless with respect to
// discovery — callers are responsible for constructing an appropriate
// Transport (direct HTTP, kubectl port-forward, or any other tunnel).
type Client struct {
	t Transport
}

// NewClient wraps the given Transport.
func NewClient(t Transport) *Client {
	return &Client{t: t}
}

// Query executes an instant PromQL query.
func (c *Client) Query(ctx context.Context, promQL string) (*QueryResult, error) {
	return c.issueQuery(ctx, "/api/v1/query", url.Values{"query": {promQL}})
}

// QueryRange executes a PromQL range query.
func (c *Client) QueryRange(ctx context.Context, promQL string, start, end time.Time, step time.Duration) (*QueryResult, error) {
	params := url.Values{
		"query": {promQL},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {fmt.Sprintf("%.0f", step.Seconds())},
	}
	return c.issueQuery(ctx, "/api/v1/query_range", params)
}

func (c *Client) issueQuery(ctx context.Context, path string, params url.Values) (*QueryResult, error) {
	body, err := c.t.Do(ctx, "GET", path, params)
	if err != nil {
		return nil, err
	}

	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("prom: parse response from %s: %w", c.t.Address(), err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prom: query error from %s: %s (%s)", c.t.Address(), pr.Error, pr.ErrorType)
	}
	return parseQueryResult(pr.Data)
}

// LabelValues returns values for a label via /api/v1/label/{label}/values.
// match entries are PromQL series selectors; zero start/end skip the window
// params; limit <= 0 skips the limit param.
func (c *Client) LabelValues(ctx context.Context, label string, matches []string, start, end time.Time, limit int) ([]string, error) {
	params := url.Values{}
	for _, m := range matches {
		params.Add("match[]", m)
	}
	if !start.IsZero() {
		params.Set("start", strconv.FormatInt(start.Unix(), 10))
	}
	if !end.IsZero() {
		params.Set("end", strconv.FormatInt(end.Unix(), 10))
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}

	body, err := c.t.Do(ctx, "GET", "/api/v1/label/"+url.PathEscape(label)+"/values", params)
	if err != nil {
		return nil, err
	}

	var pr struct {
		Status    string   `json:"status"`
		Error     string   `json:"error"`
		ErrorType string   `json:"errorType"`
		Data      []string `json:"data"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("prom: parse label values from %s: %w", c.t.Address(), err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prom: label values error from %s: %s (%s)", c.t.Address(), pr.Error, pr.ErrorType)
	}
	return pr.Data, nil
}

// MetricMetadata describes one metric as reported by /api/v1/metadata.
type MetricMetadata struct {
	Type string `json:"type"`
	Help string `json:"help"`
}

// Metadata returns metric metadata via /api/v1/metadata as Prometheus reports
// it: name → list of {type, help} (one entry per distinct target metadata;
// targets can disagree). Collapsing the list to a single entry is the caller's
// shaping decision. limit > 0 bounds the number of metrics returned
// (/api/v1/metadata?limit=) so the whole catalog isn't pulled on large
// backends; limit <= 0 fetches all.
func (c *Client) Metadata(ctx context.Context, limit int) (map[string][]MetricMetadata, error) {
	params := url.Values{}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	body, err := c.t.Do(ctx, "GET", "/api/v1/metadata", params)
	if err != nil {
		return nil, err
	}

	var pr struct {
		Status    string                      `json:"status"`
		Error     string                      `json:"error"`
		ErrorType string                      `json:"errorType"`
		Data      map[string][]MetricMetadata `json:"data"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("prom: parse metadata from %s: %w", c.t.Address(), err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prom: metadata error from %s: %s (%s)", c.t.Address(), pr.Error, pr.ErrorType)
	}
	return pr.Data, nil
}

// RuleAlert is one active alert instance of an alerting rule.
type RuleAlert struct {
	State    string            `json:"state"`
	ActiveAt string            `json:"activeAt,omitempty"`
	Value    string            `json:"value,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// Rule is a single alerting or recording rule within a RuleGroup.
type Rule struct {
	Type        string            `json:"type"` // "alerting" or "recording"
	Name        string            `json:"name"`
	Query       string            `json:"query"`
	Duration    float64           `json:"duration,omitempty"` // "for" clause, seconds
	State       string            `json:"state,omitempty"`    // alerting: firing|pending|inactive
	Health      string            `json:"health,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Alerts      []RuleAlert       `json:"alerts,omitempty"`
}

// RuleGroup is a named group of rules, the shape /api/v1/rules nests them in.
type RuleGroup struct {
	Name  string `json:"name"`
	File  string `json:"file,omitempty"`
	Rules []Rule `json:"rules"`
}

// Rules returns the rule groups from /api/v1/rules as Prometheus nests them
// (a named group holding its rules). typ filters server-side: "alert",
// "record", or "" for both — older backends ignore the param, so callers must
// not rely on it exclusively. Flattening or filtering the groups is the
// caller's shaping decision.
func (c *Client) Rules(ctx context.Context, typ string) ([]RuleGroup, error) {
	params := url.Values{}
	if typ != "" {
		params.Set("type", typ)
	}

	body, err := c.t.Do(ctx, "GET", "/api/v1/rules", params)
	if err != nil {
		return nil, err
	}

	var pr struct {
		Status    string `json:"status"`
		Error     string `json:"error"`
		ErrorType string `json:"errorType"`
		Data      struct {
			Groups []RuleGroup `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("prom: parse rules from %s: %w", c.t.Address(), err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prom: rules error from %s: %s (%s)", c.t.Address(), pr.Error, pr.ErrorType)
	}
	return pr.Data.Groups, nil
}

// ProbeReason explains a Probe result. An empty string on true = ok.
// On false, Reason indicates why discovery should skip this candidate.
type ProbeReason string

const (
	ProbeReasonTransportError ProbeReason = "transport_error" // network/HTTP failure
	ProbeReasonAuthError      ProbeReason = "auth_error"      // HTTP 401/403 — credentials rejected
	ProbeReasonNotPrometheus  ProbeReason = "not_prometheus"  // 200 but response body isn't prom JSON (captive portal, login page)
	ProbeReasonPromError      ProbeReason = "prom_error"      // prom responded with status=error
	ProbeReasonEmptyInstance  ProbeReason = "empty_instance"  // prom responded success but zero "up" results
)

// Probe checks if a Prometheus endpoint is reachable and has at least one
// active scrape target. Returns (ok, reason). When ok is true the reason is
// empty; when ok is false the reason indicates why (callers may use this
// for targeted logging — e.g., warn once per empty-instance discovery
// skip).
//
// Uses a 3-second timeout regardless of the context deadline to fail fast.
func (c *Client) Probe(ctx context.Context) (bool, ProbeReason) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	body, err := c.t.Do(probeCtx, "GET", "/api/v1/query", url.Values{"query": {"up"}})
	if err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && (httpErr.StatusCode == 401 || httpErr.StatusCode == 403) {
			return false, ProbeReasonAuthError
		}
		return false, ProbeReasonTransportError
	}

	var pr struct {
		Status string `json:"status"`
		Data   struct {
			Result []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return false, ProbeReasonNotPrometheus
	}
	if pr.Status != "success" {
		return false, ProbeReasonPromError
	}
	if len(pr.Data.Result) == 0 {
		return false, ProbeReasonEmptyInstance
	}
	return true, ""
}

func parseQueryResult(data json.RawMessage) (*QueryResult, error) {
	var head struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("prom: parse result: %w", err)
	}

	// A success response may omit "result" entirely (or send null) — some
	// PromQL-compatible backends and proxies do this for empty results. Treat
	// it as no data rather than a parse error, matching a bare Prometheus empty
	// result.
	if len(head.Result) == 0 || string(head.Result) == "null" {
		return &QueryResult{ResultType: head.ResultType, Series: []Series{}}, nil
	}

	// Scalar results (e.g. time(), scalar(sum(up))) are a single [ts, value]
	// pair rather than an array of labeled series — normalize to one labelless
	// series so callers see a uniform shape.
	if head.ResultType == "scalar" {
		var v []interface{}
		if err := json.Unmarshal(head.Result, &v); err != nil {
			return nil, fmt.Errorf("prom: parse scalar result: %w", err)
		}
		s := Series{Labels: map[string]string{}}
		if dp, ok := parseDataPoint(v); ok {
			s.DataPoints = []DataPoint{dp}
		}
		return &QueryResult{ResultType: "scalar", Series: []Series{s}}, nil
	}

	if head.ResultType == "string" {
		return nil, errors.New("prom: string result type is not supported — use a numeric query")
	}

	var raw struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][]interface{}   `json:"values"` // matrix float samples
			Value  []interface{}     `json:"value"`  // vector float sample
		}
	}
	if err := json.Unmarshal(head.Result, &raw.Result); err != nil {
		return nil, fmt.Errorf("prom: parse result: %w", err)
	}

	result := &QueryResult{
		ResultType: head.ResultType,
		Series:     make([]Series, 0, len(raw.Result)),
	}

	for _, r := range raw.Result {
		series := Series{Labels: r.Metric}

		switch head.ResultType {
		case "matrix":
			series.DataPoints = make([]DataPoint, 0, len(r.Values))
			for _, v := range r.Values {
				if dp, ok := parseDataPoint(v); ok {
					series.DataPoints = append(series.DataPoints, dp)
				}
			}
		case "vector":
			if r.Value != nil {
				if dp, ok := parseDataPoint(r.Value); ok {
					series.DataPoints = []DataPoint{dp}
				}
			}
		}

		result.Series = append(result.Series, series)
	}

	return result, nil
}

func parseDataPoint(v []interface{}) (DataPoint, bool) {
	if len(v) != 2 {
		return DataPoint{}, false
	}

	ts, ok := v[0].(float64)
	if !ok {
		return DataPoint{}, false
	}

	valStr, sok := v[1].(string)
	if !sok {
		return DataPoint{}, false
	}
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return DataPoint{}, false
	}

	return DataPoint{Timestamp: int64(ts), Value: val}, true
}
