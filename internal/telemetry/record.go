package telemetry

import (
	"net/http"
	"regexp"
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
	"investigations": true,
}

var (
	toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// Route patterns are compiled into Radar ("/api/pods/{namespace}/{name}/exec"),
	// so they carry placeholders, never the values that filled them.
	routePattern     = regexp.MustCompile(`^/api/[A-Za-z0-9_{}./-]{1,160}$`)
	componentPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{0,63}$`)
)

// Reads that are the action itself, not background polling.
var actionReads = []string{"/exec", "/logs", "/logs/stream", "/debug"}

// Named UI moments without an API call of their own.
var allowedUIEvents = map[string]bool{
	"command_palette": true,
	"search":          true,
}

var browserFamilies = []struct{ marker, family string }{
	{"Edg/", "edge"}, {"OPR/", "opera"}, {"Firefox/", "firefox"},
	{"Chrome/", "chrome"}, {"Safari/", "safari"},
}

// IsAllowedView reports whether name is a countable view. "resources:<plural>"
// counts a built-in kind's list; custom resource kinds are never named.
func IsAllowedView(name string) bool {
	if allowedViews[name] {
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
	if comp, ok := strings.CutPrefix(name, "ui_error:"); ok {
		return componentPattern.MatchString(comp)
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

// RecordActive adds minutes the UI was visible.
func RecordActive(minutes int) {
	if minutes <= 0 || minutes > 60 {
		return
	}
	increment(func(p *pending) { p.ActiveMinutes += minutes })
}

// RecordAPI counts one API request by its route pattern. Changes count as
// actions when they succeed and as errors when they fail; reads count only
// when they are the action (exec, logs) or when the server failed them.
func RecordAPI(method, pattern string, status int) {
	i := strings.Index(pattern, "/api/")
	if i < 0 {
		return
	}
	// A base path ("/radar/api/...") is the operator's own naming; drop it.
	pattern = pattern[i:]
	if !routePattern.MatchString(pattern) || strings.HasPrefix(pattern, "/api/usage-data") {
		return
	}
	key := method + " " + pattern
	read := method == http.MethodGet || method == http.MethodHead
	// Hijacked connections (exec's WebSocket) report 0 or 101.
	ok := status == 0 || (status >= 100 && status < 400)
	switch {
	case ok && (!read || isActionRead(pattern)):
		increment(func(p *pending) { p.Actions = bump(p.Actions, key) })
	case status >= 500 || (!read && status >= 400):
		errKey := key + " " + strconvStatusClass(status)
		increment(func(p *pending) { p.Errors = bump(p.Errors, errKey) })
	}
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
