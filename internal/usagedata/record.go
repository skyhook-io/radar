package usagedata

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Every recorder drops anything outside a fixed vocabulary, so a name, URL
// or free text typed by a user can never reach a report.

var allowedViews = map[string]bool{
	"home": true, "topology": true, "resources": true, "timeline": true,
	"helm": true, "helmCompare": true, "traffic": true, "cost": true,
	"capacity": true, "workload": true, "checks": true, "upgrade": true,
	"gitops": true, "applications": true, "compare": true, "issues": true,
	"investigations": true, "cnpg": true,
}

// Resource drawer panels. The drawer's Timeline tab carries the resource's
// events (it replaced the Events tab), so it counts as panel:events.
var panelViews = map[string]bool{
	"panel:logs": true, "panel:yaml": true, "panel:metrics": true, "panel:events": true,
}

var (
	toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// Route patterns are compiled into Radar ("/api/pods/{namespace}/{name}/exec"),
	// so they carry placeholders, never the values that filled them.
	routePattern = regexp.MustCompile(`^/api/[A-Za-z0-9_{}./-]{1,160}$`)
)

// Only standard methods name an action; any other string is the client's.
var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

// Reads that are the action itself, not background polling.
var actionReads = []string{"/exec", "/logs", "/logs/stream", "/debug"}

// Named UI moments without an API call of their own.
var allowedUIEvents = map[string]bool{
	"command_palette":          true,
	"search":                   true,
	"issue_action:open":        true,
	"issue_action:investigate": true,
	"issue_action:capacity":    true,
}

// The screens a crash is counted under: CRASH_LABELS in web/src/App.tsx
// (workspace labels included), plus the upgrade-impact page.
var crashScreens = map[string]bool{
	"Home": true, "Topology": true, "Resources": true, "Timeline": true,
	"Issues": true, "Helm": true, "HelmCompare": true, "Traffic": true,
	"Cost": true, "Capacity": true, "Checks": true, "GitOps": true,
	"Applications": true, "Workload": true, "Compare": true,
	"Investigations": true, "Upgrade": true, "CloudNativePG": true,
}

// Statuses an error is counted under exactly; any other failure keeps its
// class, 4xx or 5xx.
var exactStatuses = map[int]bool{
	401: true, 403: true, 404: true, 409: true, 429: true,
	500: true, 502: true, 503: true, 504: true,
}

// Routes whose successful requests also count the built-in kind they acted
// on, as "<verb>:<plural>".
var kindActions = map[string]string{
	"DELETE /api/resources/{kind}/{namespace}/{name}":       "delete",
	"POST /api/workloads/{kind}/{namespace}/{name}/restart": "restart",
}

// k8s.ClassifyError's vocabulary.
var connectionFailureTypes = map[string]bool{
	"config": true, "auth": true, "auth-rejected": true, "auth-plugin-stuck": true,
	"rbac": true, "network": true, "timeout": true, "tls": true, "unknown": true,
}

// The traffic sources Radar can read flows from.
var trafficSources = map[string]bool{
	"hubble": true, "caretta": true, "istio": true, "beyla": true,
}

var browserFamilies = []struct{ marker, family string }{
	{"Edg/", "edge"}, {"OPR/", "opera"}, {"Firefox/", "firefox"},
	{"Chrome/", "chrome"}, {"Safari/", "safari"},
}

// IsAllowedView reports whether name is a countable view. "resources:<plural>"
// counts a built-in kind's list; custom resource kinds are never named.
func IsAllowedView(name string) bool {
	if allowedViews[name] || panelViews[name] {
		return true
	}
	if kind, ok := strings.CutPrefix(name, "resources:"); ok {
		_, builtin := resourceid.BuiltinForName(kind)
		return builtin
	}
	return false
}

// IsAllowedUIEvent reports whether name is a countable UI event.
func IsAllowedUIEvent(name string) bool {
	if allowedUIEvents[name] {
		return true
	}
	if screen, ok := strings.CutPrefix(name, "ui_error:"); ok {
		return crashScreens[screen]
	}
	return false
}

// RecordView counts one opening of a Radar view.
func RecordView(name string) {
	if !IsAllowedView(name) {
		return
	}
	if kind, ok := strings.CutPrefix(name, "resources:"); ok {
		name = "resources:" + strings.ToLower(kind)
	}
	increment(func(p *pending) { p.Views = bump(p.Views, name) })
}

// RecordUIEvent counts one UI moment, such as opening the command palette or
// a view crashing into its error boundary.
func RecordUIEvent(name string) {
	if IsAllowedUIEvent(name) {
		increment(func(p *pending) { p.UIEvents = bump(p.UIEvents, name) })
	}
}

// RecordMCPTool counts one MCP tool call.
func RecordMCPTool(name string) {
	if toolNamePattern.MatchString(name) {
		increment(func(p *pending) { p.MCPTools = bump(p.MCPTools, name) })
	}
}

// RecordSession counts one opening of the UI and the browser family it ran in.
func RecordSession(userAgent string) {
	family := BrowserFamily(userAgent)
	increment(func(p *pending) {
		p.Sessions++
		p.Browsers = bump(p.Browsers, family)
	})
}

// RecordActive adds minutes the UI was in use.
func RecordActive(minutes int) {
	RecordActiveView(minutes, "")
}

// RecordActiveView adds minutes the UI was in use, and counts view once if
// the period has no views yet: a window left open since an earlier period is
// used without navigating, and its report would otherwise show time but no
// views.
func RecordActiveView(minutes int, view string) {
	if minutes <= 0 || minutes > 60 {
		return
	}
	if !IsAllowedView(view) {
		view = ""
	}
	if kind, ok := strings.CutPrefix(view, "resources:"); ok {
		view = "resources:" + strings.ToLower(kind)
	}
	increment(func(p *pending) {
		p.ActiveMinutes += minutes
		if view != "" && len(p.Views) == 0 {
			p.Views = bump(p.Views, view)
		}
	})
}

// RecordConnectionFailure counts one failure to connect to a cluster by its
// k8s.ClassifyError type. Anything outside that vocabulary counts as unknown.
func RecordConnectionFailure(errorType string) {
	if !connectionFailureTypes[errorType] {
		errorType = "unknown"
	}
	increment(func(p *pending) { p.ConnectionFailures = bump(p.ConnectionFailures, errorType) })
}

// TrafficSource names the traffic source in use, or "none".
func TrafficSource(name string) string {
	if trafficSources[name] {
		return name
	}
	return "none"
}

// RecordAPI counts one API request by its route pattern, which the caller has
// already stripped of any base path: that prefix is the operator's own naming.
// Changes count as actions when they succeed and as errors when they fail;
// reads count only when they are the action (exec, logs) or when the server
// failed them.
func RecordAPI(method, pattern string, status int) {
	RecordRequest(APIRequest{Method: method, Pattern: pattern, Status: status})
}

// APIRequest is one API request as the router saw it.
type APIRequest struct {
	Method  string
	Pattern string
	Status  int
	// Automatic marks a request the UI made on its own, such as a background
	// probe or a reconnect attempt. It is not something the user did, so it
	// never counts as an action; it still counts when it fails.
	Automatic bool
	// NotConnected marks the answer "no cluster is connected yet". That is a
	// state the UI already shows, not a failed request.
	NotConnected bool
	// Kind and Group are the request's {kind} URL parameter and group query
	// parameter. Only kindActions routes read them, and only a built-in kind
	// is ever named.
	Kind  string
	Group string
}

// RecordRequest counts one API request; see RecordAPI.
func RecordRequest(r APIRequest) {
	if !knownMethods[r.Method] || !routePattern.MatchString(r.Pattern) || strings.HasPrefix(r.Pattern, "/api/usage-data") {
		return
	}
	key := r.Method + " " + r.Pattern
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	// Hijacked connections (exec's WebSocket) report 0 or 101.
	ok := r.Status == 0 || (r.Status >= 100 && r.Status < 400)
	switch {
	case ok && (!read || isActionRead(r.Pattern)):
		if r.Automatic {
			return
		}
		kindKey := actionKindKey(key, r.Kind, r.Group)
		increment(func(p *pending) {
			p.Actions = bump(p.Actions, key)
			if kindKey != "" {
				p.ActionKinds = bump(p.ActionKinds, kindKey)
			}
		})
	case ok || r.NotConnected:
	case r.Status >= 500 || (!read && r.Status >= 400):
		errKey := key + " " + strconvStatusClass(r.Status)
		increment(func(p *pending) { p.Errors = bump(p.Errors, errKey) })
	}
}

// actionKindKey names the built-in kind a kindActions route acted on. A group
// that isn't the built-in's own means a custom resource sharing its name,
// which is never named.
func actionKindKey(routeKey, kind, group string) string {
	verb, ok := kindActions[routeKey]
	if !ok {
		return ""
	}
	b, ok := resourceid.BuiltinForName(kind)
	if !ok || (group != "" && group != b.Group) {
		return ""
	}
	return verb + ":" + b.Resource
}

func isActionRead(pattern string) bool {
	for _, suffix := range actionReads {
		if strings.HasSuffix(pattern, suffix) {
			return true
		}
	}
	return false
}

func strconvStatusClass(status int) string {
	if exactStatuses[status] {
		return strconv.Itoa(status)
	}
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	}
	return "other"
}

// BrowserFamily names the browser without its version or platform detail.
func BrowserFamily(userAgent string) string {
	for _, b := range browserFamilies {
		if strings.Contains(userAgent, b.marker) {
			return b.family
		}
	}
	return "other"
}

// Recording reports whether usage is being counted right now; callers on hot
// paths check it before doing any work.
func Recording() bool {
	c := current()
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Recording()
}

func increment(fn func(*pending)) {
	c := current()
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Recording() {
		return
	}
	fn(&c.p)
	c.dirty = true
}

func bump(m map[string]int, key string) map[string]int {
	if m == nil {
		m = map[string]int{}
	}
	m[key]++
	return m
}

// SortedUnique returns names sorted with duplicates removed.
func SortedUnique(names []string) []string {
	seen := map[string]int{}
	for _, n := range names {
		if n != "" {
			seen[n]++
		}
	}
	return sortedKeys(seen)
}
