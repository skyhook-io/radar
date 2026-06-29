package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/common/model"

	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

// promQueryTimeout is the default per-call budget for the read tools. var, not
// const, so tests can shrink it to exercise the timeout paths.
var promQueryTimeout = 30 * time.Second

const (
	promMaxQueryTimeout      = 180 * time.Second
	promDefaultSince         = time.Hour
	promDefaultMaxPoints     = 300
	promHardMaxPoints        = 600
	promMinStep              = 15 * time.Second
	promDefaultResponseBytes = 64 << 10
	promDiscoverDefaultLimit = 100
	promDiscoverMaxLimit     = 500
	promDiscoverLookback     = time.Hour
	promMetadataLimit        = 5000
	promRulesDefaultLimit    = 50
	promRulesMaxLimit        = 200
)

type queryPrometheusInput struct {
	Query     string `json:"query" jsonschema:"PromQL query to execute. For metrics with many series (>10), wrap with topk(5, ...) to bound the result. Use discover_metrics first when unsure of metric or label names."`
	Type      string `json:"type,omitempty" jsonschema:"instant (default) for current values, or range for time series history"`
	Since     string `json:"since,omitempty" jsonschema:"range queries: how far back to look, e.g. 30m, 1h, 24h, 7d (default 1h). Ignored for instant"`
	Start     string `json:"start,omitempty" jsonschema:"range queries: RFC3339 start time. Overrides since when set (use with end to zoom into an incident window)"`
	End       string `json:"end,omitempty" jsonschema:"range queries: RFC3339 end time (default now)"`
	Step      string `json:"step,omitempty" jsonschema:"range queries: resolution like 30s, 5m. Omit to auto-calculate; the server lowers resolution when the result would exceed the point budget"`
	MaxPoints int    `json:"max_points,omitempty" jsonschema:"range queries: max data points per series (default 300, max 600). Raise only for 1-3 series when hunting short spikes; narrow the window instead when possible"`
	Timeout   int    `json:"timeout,omitempty" jsonschema:"query timeout in seconds (default 30, max 180). Raise only when a complex long-range query timed out at the default — prefer simplifying the query"`
}

type getPrometheusRulesInput struct {
	Type  string `json:"type,omitempty" jsonschema:"alert for alerting rules, record for recording rules. Omit for both"`
	Name  string `json:"name,omitempty" jsonschema:"case-insensitive substring filter on rule name, e.g. CrashLoop"`
	Group string `json:"group,omitempty" jsonschema:"case-insensitive substring filter on rule group name"`
	State string `json:"state,omitempty" jsonschema:"alerting rules only: firing, pending, or inactive. Implies type=alert"`
	Limit int    `json:"limit,omitempty" jsonschema:"max rules returned (default 50, max 200)"`
}

// ruleWithGroup is the flat shape the tool emits: a prom.Rule with its group
// name stamped on, so the model gets a flat list instead of walking groups.
type ruleWithGroup struct {
	Group string `json:"group"`
	prom.Rule
}

type promRulesResponse struct {
	Count     int             `json:"count"`
	Rules     []ruleWithGroup `json:"rules"`
	Truncated bool            `json:"truncated,omitempty"`
	Note      string          `json:"note,omitempty"`
}

type discoverMetricsInput struct {
	Match string `json:"match,omitempty" jsonschema:"PromQL series selector to filter, e.g. {__name__=~\"node_cpu.*|node_memory.*\"} or {namespace=\"payments\"}. Combine patterns with regex | to reduce calls. REQUIRED when label is empty (unfiltered metric-name listing is rarely useful)"`
	Label string `json:"label,omitempty" jsonschema:"discover values of this label instead of metric names, e.g. namespace, pod, job, instance"`
	Limit int    `json:"limit,omitempty" jsonschema:"max values returned (default 100, max 500)"`
}

type promQueryResponse struct {
	Query       string          `json:"query"`
	Type        string          `json:"type"`
	Start       string          `json:"start,omitempty"`
	End         string          `json:"end,omitempty"`
	Step        string          `json:"step,omitempty"`
	ResultType  string          `json:"resultType,omitempty"`
	SeriesCount int             `json:"seriesCount"`
	Series      []prom.Series   `json:"series"`
	Truncated   bool            `json:"truncated,omitempty"`
	Summary     json.RawMessage `json:"summary,omitempty"`
	Note        string          `json:"note,omitempty"`
}

type promMetricInfo struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Help string `json:"help,omitempty"`
}

type discoverMetricsResponse struct {
	Match     string           `json:"match,omitempty"`
	Label     string           `json:"label,omitempty"`
	Count     int              `json:"count"`
	Metrics   []promMetricInfo `json:"metrics,omitempty"`
	Values    []string         `json:"values,omitempty"`
	Truncated bool             `json:"truncated,omitempty"`
	Note      string           `json:"note,omitempty"`
	Usage     string           `json:"usage,omitempty"`
}

func handleQueryPrometheus(ctx context.Context, req *mcp.CallToolRequest, input queryPrometheusInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(input.Query) == "" {
		return nil, nil, fmt.Errorf("query is required — a PromQL expression, e.g. topk(5, rate(container_cpu_usage_seconds_total[5m]))")
	}
	queryType := input.Type
	if queryType == "" {
		queryType = "instant"
	}
	if queryType != "instant" && queryType != "range" {
		return nil, nil, fmt.Errorf("type must be instant or range, got %q", input.Type)
	}

	timeout := resolveQueryTimeout(input.Timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	p, connErr := connectProm(ctx)
	if connErr != nil {
		return nil, nil, connErr
	}

	resp := promQueryResponse{Query: input.Query, Type: queryType}
	var result *prom.QueryResult
	var err error

	if queryType == "instant" {
		result, err = p.Query(ctx, input.Query)
	} else {
		var start, end time.Time
		start, end, err = resolveRange(input.Since, input.Start, input.End)
		if err != nil {
			return nil, nil, err
		}
		step, stepErr := adjustStep(end.Sub(start), input.Step, input.MaxPoints)
		if stepErr != nil {
			return nil, nil, stepErr
		}
		resp.Start = start.UTC().Format(time.RFC3339)
		resp.End = end.UTC().Format(time.RFC3339)
		resp.Step = step.String()
		result, err = p.QueryRange(ctx, input.Query, start, end, step)
	}
	if err != nil {
		return nil, nil, promQueryError(ctx, input.Query, timeout, err)
	}

	resp.ResultType = result.ResultType
	resp.SeriesCount = len(result.Series)
	resp.Series = []prom.Series{}
	if len(result.Series) == 0 {
		resp.Note = "query returned no data — verify metric and label names with discover_metrics"
		return toJSONResult(resp)
	}

	seriesBytes, err := json.Marshal(result.Series)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal series: %w", err)
	}
	if len(seriesBytes) > maxPromResponseBytes() {
		resp.Truncated = true
		resp.Summary = summarizeLargeResult(result, input.Query, queryType == "range")
		resp.Note = "result too large to return raw — do NOT answer from partial data. Retry with the summary's suggestion (topk), a tighter label selector, or a shorter window; labelCardinality shows which label to constrain."
		return toJSONResult(resp)
	}

	resp.Series = result.Series
	return toJSONResult(resp)
}

func handleDiscoverMetrics(ctx context.Context, req *mcp.CallToolRequest, input discoverMetricsInput) (*mcp.CallToolResult, any, error) {
	if input.Label == "" && strings.TrimSpace(input.Match) == "" {
		return nil, nil, errors.New("match is required when listing metric names — unbounded listing returns thousands of entries; " +
			`use a selector like {__name__=~"node_cpu.*"} or {namespace="payments"}`)
	}

	limit := input.Limit
	if limit <= 0 {
		limit = promDiscoverDefaultLimit
	}
	if limit > promDiscoverMaxLimit {
		limit = promDiscoverMaxLimit
	}

	ctx, cancel := context.WithTimeout(ctx, promQueryTimeout)
	defer cancel()

	p, connErr := connectProm(ctx)
	if connErr != nil {
		return nil, nil, connErr
	}

	var matches []string
	if strings.TrimSpace(input.Match) != "" {
		matches = []string{input.Match}
	}
	// Lookback window keeps dead series (rotated pods, removed exporters)
	// from bloating discovery results.
	end := time.Now()
	start := end.Add(-promDiscoverLookback)

	label := input.Label
	if label == "" {
		label = "__name__"
	}
	// Request one past the limit so a result of exactly `limit` can be told
	// apart from a genuinely truncated one — asking for `limit` alone makes a
	// complete-at-limit result indistinguishable from "more exist".
	values, err := p.LabelValues(ctx, label, matches, start, end, limit+1)
	if err != nil {
		return nil, nil, promDiscoverError(ctx, input.Match, err)
	}

	// The limit is also enforced client-side: the server-side limit param only
	// landed in Prometheus 2.55, and older Prometheus / Thanos / Mimir builds
	// silently ignore unknown params — without this cap a broad match on such
	// backends returns an unbounded payload with no truncation signal.
	truncated := len(values) > limit
	if truncated {
		values = values[:limit]
	}

	resp := discoverMetricsResponse{
		Match: input.Match,
		Label: input.Label,
		Count: len(values),
	}
	if truncated {
		resp.Truncated = true
		// Count is the post-cap length (== limit); frame it as a floor so the
		// model doesn't read it as an authoritative total. In label mode the
		// caller may have supplied no match, so suggest both levers.
		if input.Label != "" {
			resp.Note = fmt.Sprintf("showing the first %d values — more exist; raise limit, or add/tighten a match selector to scope", limit)
		} else {
			resp.Note = fmt.Sprintf("showing the first %d names — more exist; use a more specific match selector", limit)
		}
	}

	if input.Label != "" {
		resp.Values = values
		return toJSONResult(resp)
	}

	// Metadata enrichment is best-effort: recording rules and remote-write
	// series have no metadata, and a metadata fetch failure must not turn a
	// successful discovery into an error. The limit bounds the catalog pulled
	// on large backends (names beyond it simply degrade to empty type/help).
	metadata, mdErr := p.Metadata(ctx, promMetadataLimit)
	if mdErr != nil {
		log.Printf("[mcp] discover_metrics: metadata fetch failed, returning bare names: %v", mdErr)
	}
	resp.Metrics = make([]promMetricInfo, 0, len(values))
	hasCounter := false
	for _, name := range values {
		info := promMetricInfo{Name: name}
		// Prometheus reports a list per name (targets can disagree); the first
		// entry is a fine default for the enrichment hint.
		if entries, ok := metadata[name]; ok && len(entries) > 0 {
			info.Type = entries[0].Type
			info.Help = entries[0].Help
			if info.Type == "counter" {
				hasCounter = true
			}
		}
		resp.Metrics = append(resp.Metrics, info)
	}
	// JIT reminder at the moment the model is about to compose a query: a
	// counter is cumulative, so querying it raw is almost always a mistake.
	if hasCounter {
		resp.Usage = "some results are counters (type=counter) — they only ever increase, so wrap them in rate(metric[5m]) before querying; gauges can be queried directly"
	}
	return toJSONResult(resp)
}

func handleGetPrometheusRules(ctx context.Context, req *mcp.CallToolRequest, input getPrometheusRulesInput) (*mcp.CallToolResult, any, error) {
	if input.Type != "" && input.Type != "alert" && input.Type != "record" {
		return nil, nil, fmt.Errorf("type must be alert or record, got %q", input.Type)
	}
	if input.State != "" && input.State != "firing" && input.State != "pending" && input.State != "inactive" {
		return nil, nil, fmt.Errorf("state must be firing, pending, or inactive, got %q", input.State)
	}

	if input.State != "" && input.Type == "record" {
		return nil, nil, errors.New("state filters alerting rules only — it cannot combine with type=record (recording rules have no state); drop one")
	}

	limit := input.Limit
	if limit <= 0 {
		limit = promRulesDefaultLimit
	}
	if limit > promRulesMaxLimit {
		limit = promRulesMaxLimit
	}

	ctx, cancel := context.WithTimeout(ctx, promQueryTimeout)
	defer cancel()

	p, connErr := connectProm(ctx)
	if connErr != nil {
		return nil, nil, connErr
	}

	groups, err := p.Rules(ctx, input.Type)
	if err != nil {
		var httpErr *prom.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			return nil, nil, errors.New("this metrics backend does not expose /api/v1/rules (common for managed offerings like Amazon Managed Prometheus) — alerting rules are not queryable here")
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, nil, fmt.Errorf("fetching prometheus rules timed out after %s", promQueryTimeout)
		}
		// Other HTTP errors (500/503/401/422) go through the shared sanitizer
		// so the backend URL in the raw *prom.HTTPError doesn't leak.
		if e := promHTTPError(err, "rules request", ""); e != nil {
			return nil, nil, e
		}
		return nil, nil, err
	}

	// Flatten the nested groups into one list with the group stamped on each
	// rule (the shape the model reasons over), filtering client-side: older
	// backends ignore the server-side type param, and name/group/state have no
	// server-side equivalent at all.
	var filtered []ruleWithGroup
	for _, g := range groups {
		for _, r := range g.Rules {
			if input.Type == "alert" && r.Type != "alerting" {
				continue
			}
			if input.Type == "record" && r.Type != "recording" {
				continue
			}
			if input.State != "" && r.State != input.State {
				continue
			}
			if input.Name != "" && !strings.Contains(strings.ToLower(r.Name), strings.ToLower(input.Name)) {
				continue
			}
			if input.Group != "" && !strings.Contains(strings.ToLower(g.Name), strings.ToLower(input.Group)) {
				continue
			}
			filtered = append(filtered, ruleWithGroup{Group: g.Name, Rule: r})
		}
	}

	var resp promRulesResponse
	if len(filtered) > limit {
		filtered = filtered[:limit]
		resp.Truncated = true
		resp.Note = "narrow with name, group, state, or type filters"
	}
	if filtered == nil {
		filtered = []ruleWithGroup{}
	}
	resp.Rules = filtered
	resp.Count = len(filtered)
	if len(filtered) == 0 {
		resp.Note = "no rules matched — drop filters to see what exists, or the backend may have no rules configured"
	}
	return toJSONResult(resp)
}

// resolveRange computes the [start, end] window: explicit RFC3339 start/end
// win over since; since defaults to 1h back from now.
func resolveRange(since, startStr, endStr string) (time.Time, time.Time, error) {
	now := time.Now()

	end := now
	if endStr != "" {
		t, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end %q: must be RFC3339, e.g. 2026-01-02T15:04:05Z", endStr)
		}
		end = t
	}

	var start time.Time
	if startStr != "" {
		t, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start %q: must be RFC3339, e.g. 2026-01-02T15:04:05Z", startStr)
		}
		start = t
	} else {
		window := promDefaultSince
		if since != "" {
			d, err := parsePromDuration(since)
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
			window = d
		}
		start = end.Add(-window)
	}

	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("start (%s) must be before end (%s)",
			start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	}
	return start, end, nil
}

// parsePromDuration resolves the duration strings models reach for, which no
// single parser covers: Go durations (30m, 1h30m, 100ms, 1.5h), Prometheus
// durations including compound and week/year units (7d, 1d12h, 2w, 1y), and
// bare fractional days (1.5d).
func parsePromDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	d, ok := parsePromDurationValue(s)
	if !ok {
		return 0, fmt.Errorf("invalid duration %q: use formats like 30m, 6h, 7d, 1d12h, 2w", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", s)
	}
	return d, nil
}

// parsePromDurationValue tries three grammars, widest compatibility first:
// Go's time.ParseDuration, then Prometheus's model.ParseDuration (adds d/w/y
// and compound units), then a bare fractional-day count that neither accepts.
func parsePromDurationValue(s string) (time.Duration, bool) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, true
	}
	if d, err := model.ParseDuration(s); err == nil {
		return time.Duration(d), true
	}
	if strings.HasSuffix(s, "d") {
		// Cap the magnitude before converting: a huge float day count
		// overflows time.Duration with implementation-defined results.
		days, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err == nil && !math.IsNaN(days) && !math.IsInf(days, 0) && math.Abs(days) <= 3650 {
			return time.Duration(days * 24 * float64(time.Hour)), true
		}
	}
	return 0, false
}

// adjustStep resolves the effective range-query step so the sample count
// never exceeds the point budget. Two Prometheus realities shape the math:
// range results include both endpoints (floor(window/step)+1 samples), so the
// budget divides by maxPoints-1; and the wire format truncates steps to whole
// seconds, so fractional steps would execute at a different resolution than
// the one echoed back to the model — everything rounds up to whole seconds.
func adjustStep(window time.Duration, stepStr string, maxPoints int) (time.Duration, error) {
	if maxPoints <= 0 {
		maxPoints = promDefaultMaxPoints
	}
	if maxPoints > promHardMaxPoints {
		log.Printf("[mcp] query_prometheus: max_points %d clamped to %d", maxPoints, promHardMaxPoints)
		maxPoints = promHardMaxPoints
	}
	if maxPoints < 2 {
		maxPoints = 2
	}

	floor := time.Duration(math.Ceil(window.Seconds()/float64(maxPoints-1))) * time.Second

	var step time.Duration
	if stepStr != "" {
		d, err := parsePromDuration(stepStr)
		if err != nil {
			return 0, fmt.Errorf("invalid step: %w", err)
		}
		step = d
	}
	if step == 0 {
		step = promMinStep
	}
	if step < floor {
		step = floor
	}
	return time.Duration(math.Ceil(step.Seconds())) * time.Second, nil
}

// summarizeLargeResult replaces an oversized series payload with a
// cardinality breakdown plus a ready-to-run rewrite, so the model can
// self-correct instead of answering from silently cut data. isRange steers the
// suggestion: with few series the bytes are points-driven, where topk is a
// no-op and a shorter window / larger step is the real fix.
func summarizeLargeResult(result *prom.QueryResult, query string, isRange bool) json.RawMessage {
	totalPoints := 0
	cardinality := map[string]map[string]struct{}{}
	for _, s := range result.Series {
		totalPoints += len(s.DataPoints)
		for k, v := range s.Labels {
			if cardinality[k] == nil {
				cardinality[k] = map[string]struct{}{}
			}
			cardinality[k][v] = struct{}{}
		}
	}

	type labelCount struct {
		label string
		count int
	}
	counts := make([]labelCount, 0, len(cardinality))
	for k, vals := range cardinality {
		counts = append(counts, labelCount{k, len(vals)})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].count != counts[j].count {
			return counts[i].count > counts[j].count
		}
		return counts[i].label < counts[j].label
	})

	// Hand-built JSON keeps labelCardinality in descending order — the first
	// key is the label that explodes the result, which is the whole point of
	// the summary. A map would marshal in random order.
	var b strings.Builder
	b.WriteString(`{"seriesCount":`)
	b.WriteString(strconv.Itoa(len(result.Series)))
	b.WriteString(`,"totalDataPoints":`)
	b.WriteString(strconv.Itoa(totalPoints))
	b.WriteString(`,"labelCardinality":{`)
	for i, lc := range counts {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(lc.label)
		b.Write(key)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(lc.count))
	}
	b.WriteString(`},"suggestion":`)
	n := len(result.Series)
	topkN := 5
	if n < topkN {
		topkN = n
	}
	var suggestionText string
	if isRange && n <= 5 {
		suggestionText = fmt.Sprintf("only %d series yet still oversized — points-per-series dominate, so topk won't help; use a shorter window (smaller since) or a larger step", n)
	} else {
		suggestionText = fmt.Sprintf("topk(%d, %s)", topkN, query)
	}
	suggestion, _ := json.Marshal(suggestionText)
	b.Write(suggestion)
	b.WriteByte('}')
	return json.RawMessage(b.String())
}

func connectProm(ctx context.Context) (*prom.Client, error) {
	client := prometheus.GetClient()
	if client == nil {
		return nil, errors.New("prometheus is not initialized — radar is not connected to a cluster yet")
	}
	if _, _, err := client.EnsureConnected(ctx); err != nil {
		return nil, promNotConnectedError(client, err)
	}
	p := client.PromForMCP()
	if p == nil {
		return nil, errors.New("prometheus connection was reset — retry")
	}
	return p, nil
}

// promNotConnectedError turns a discovery/connection failure into an
// actionable message including current status.
func promNotConnectedError(client *prometheus.Client, err error) error {
	status := client.GetStatus()
	detail := ""
	if status.Address != "" {
		detail = fmt.Sprintf(" (last address: %s)", status.Address)
	}
	return fmt.Errorf("prometheus is not connected%s: %v — radar auto-discovers Prometheus in the cluster; "+
		"set --prometheus-url (with --prometheus-header for auth) or check Settings → Prometheus in the radar UI", detail, err)
}

// resolveQueryTimeout clamps the model-requested timeout to [default, max].
func resolveQueryTimeout(secs int) time.Duration {
	if secs <= 0 {
		return promQueryTimeout
	}
	maxSecs := int(promMaxQueryTimeout / time.Second)
	if secs > maxSecs {
		log.Printf("[mcp] query_prometheus: timeout %ds clamped to %s", secs, promMaxQueryTimeout)
		secs = maxSecs
	}
	return time.Duration(secs) * time.Second
}

// promHTTPError formats a non-2xx Prometheus response as status + body, echoing
// the offending input (noun is "query"/"match"/...), WITHOUT the backend URL
// embedded in the raw *prom.HTTPError. Every caller funnels HTTP errors through
// here so the URL sanitization can't drift between tools. Returns nil when err
// is not an *prom.HTTPError, so callers fall through.
func promHTTPError(err error, noun, value string) error {
	var httpErr *prom.HTTPError
	if !errors.As(err, &httpErr) {
		return nil
	}
	body := strings.TrimSpace(string(httpErr.Body))
	if value == "" {
		return fmt.Errorf("prometheus returned %d for %s: %s", httpErr.StatusCode, noun, body)
	}
	return fmt.Errorf("prometheus returned %d for %s %q: %s", httpErr.StatusCode, noun, value, body)
}

// promQueryError maps query failures to self-correctable messages. A timeout
// gets a tailored hint (there's no body/status to carry the info); HTTP errors
// go through promHTTPError (status + body, no URL leak).
func promQueryError(ctx context.Context, query string, timeout time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("prometheus query timed out after %s — narrow the time window, add aggregation (sum/avg by), or wrap with topk(5, ...); raise timeout (max 180) only as a last resort: %s",
			timeout, query)
	}
	if e := promHTTPError(err, "query", query); e != nil {
		return e
	}
	return err
}

// promDiscoverError is the discovery twin of promQueryError — the timeout hint
// points at the match selector rather than the query.
func promDiscoverError(ctx context.Context, match string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("prometheus discovery timed out after %s — add or tighten the match selector to reduce the series scanned", promQueryTimeout)
	}
	if e := promHTTPError(err, "match", match); e != nil {
		return e
	}
	return err
}

func maxPromResponseBytes() int {
	if v := os.Getenv("RADAR_MCP_PROM_MAX_RESPONSE_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return promDefaultResponseBytes
}
