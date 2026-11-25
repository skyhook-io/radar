package openobserve

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

// SetupRequestTimeout bounds each request Setup sends to OpenObserve.
const SetupRequestTimeout = 30 * time.Second

// Setup outcomes.
const (
	SetupSaved    = "saved"
	SetupConfirm  = "confirm"
	SetupNotFound = "not_found"
)

// SetupRequest is one "Test and save" from Settings → Metrics: either the
// saved connection (UseSaved) or the connection fields, and optionally the
// cluster name the user confirmed.
type SetupRequest struct {
	UseSaved     bool           `json:"useSaved"`
	BaseURL      string         `json:"baseUrl"`
	Org          string         `json:"org"`
	User         string         `json:"user"`
	Token        string         `json:"token"`
	CACertFile   string         `json:"caCertFile"`
	ClusterLabel string         `json:"clusterLabel"`
	Confirm      *oo.LabelValue `json:"confirm,omitempty"`
	// ClusterUID is the cluster the caller was shown; the handler refuses
	// the request when Radar is now on another one.
	ClusterUID string `json:"clusterUid,omitempty"`
}

// MatchView is how a cluster is found in OpenObserve, for display.
type MatchView struct {
	Label string `json:"label"`
	Value string `json:"value"`
	By    string `json:"by"`
}

// SetupNotes describe a saved match.
type SetupNotes struct {
	// Collector: the data comes from the OpenObserve collector.
	Collector bool `json:"collector"`
	// Missing are the metric families with no data.
	Missing        []string `json:"missing"`
	Warning        string   `json:"warning,omitempty"`
	VersionWarning string   `json:"versionWarning,omitempty"`
}

// SetupResult is what a setup attempt found. Only SetupSaved saved anything.
type SetupResult struct {
	Outcome      string          `json:"outcome"`
	Match        *MatchView      `json:"match,omitempty"`
	Suggestion   *oo.LabelValue  `json:"suggestion,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	ClusterNames []oo.LabelValue `json:"clusterNames,omitempty"`
	ClusterUID   string          `json:"clusterUid"`
	Notes        SetupNotes      `json:"notes"`
}

// SetupError is a failure the user can act on: invalid input, a rejected
// login, an unreachable OpenObserve. Its message is a plain sentence that
// never carries the token.
type SetupError struct{ Message string }

func (e *SetupError) Error() string { return e.Message }

func setupErrorf(format string, args ...any) error {
	return &SetupError{Message: fmt.Sprintf(format, args...)}
}

// SetupDeps are what Setup reads and writes, injectable for tests.
type SetupDeps struct {
	// Existing is the saved connection, nil when there is none.
	Existing *config.OpenObserveConfig
	// ContextName is saved with the match, for display.
	ContextName string
	// UID is the cluster's kube-system namespace UID; read through Kube when empty.
	UID string
	// Kube reads the cluster: its UID and the Pod sample for a name match.
	Kube kubernetes.Interface
	// Transport builds the raw transport to OpenObserve; nil uses the
	// connection's HTTP transport.
	Transport func(oo.Connection) (prom.Transport, error)
	// Save applies update to the config file. Setup calls it at most once,
	// and only after the cluster was found.
	Save func(update func(*config.Config)) error
	Now  func() time.Time
}

var labelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Setup tests a connection and finds the current cluster in OpenObserve. It
// saves the connection and the cluster's match only when the login works and
// the cluster is found by ID or by a name the user confirmed; a name found
// without confirmation comes back as SetupConfirm, and a cluster not found as
// SetupNotFound with the cluster names OpenObserve has.
func Setup(ctx context.Context, deps SetupDeps, req SetupRequest) (SetupResult, error) {
	entry, resetMatches, err := setupEntry(deps.Existing, req)
	if err != nil {
		return SetupResult{}, err
	}
	conn, err := Connection(entry)
	if err != nil {
		return SetupResult{}, &SetupError{Message: connectionProblem(err)}
	}
	newTransport := deps.Transport
	if newTransport == nil {
		newTransport = func(c oo.Connection) (prom.Transport, error) { return c.NewHTTPTransport(SetupRequestTimeout) }
	}
	raw, err := newTransport(conn)
	if err != nil {
		return SetupResult{}, &SetupError{Message: connectionProblem(err)}
	}
	fail := func(what string, err error) error {
		return &SetupError{Message: describeFailure(what, conn, err)}
	}
	if _, err := prom.NewClient(raw).Query(ctx, "vector(1)"); err != nil {
		return SetupResult{}, fail("", err)
	}

	uid := deps.UID
	if uid == "" {
		if deps.Kube == nil {
			return SetupResult{}, errors.New("no Kubernetes client to read the cluster ID with")
		}
		if uid, err = ReadClusterUID(ctx, deps.Kube); err != nil {
			return SetupResult{}, setupErrorf("Radar couldn't read this cluster's ID: %s.", err)
		}
	}
	result := SetupResult{ClusterUID: uid, Notes: SetupNotes{Missing: []string{}}}

	ids, err := oo.DiscoverIDs(ctx, raw, []string{uid})
	if err != nil {
		return SetupResult{}, fail("look for this cluster in", err)
	}
	var match oo.ClusterMatch
	if m, ok := ids[uid]; ok {
		if missing, err := oo.DetectFamilies(ctx, raw, m.Options); err == nil {
			m.Missing = missing
		} else {
			log.Printf("[openobserve] Checking which metric families have data: %s", redact(err.Error(), conn.Token))
		}
		match = m
	} else {
		var sample oo.PodSample
		if deps.Kube != nil {
			sample, err = BuildPodSample(ctx, deps.Kube)
		} else {
			err = errors.New("no Kubernetes client")
		}
		if err != nil {
			result.Outcome = SetupNotFound
			result.Reason = fmt.Sprintf("OpenObserve has no metrics with this cluster's ID, and Radar couldn't sample its running Pods to look for its name: %s.", err)
			result.ClusterNames = listNames(ctx, raw, entry.ClusterLabel, conn.Token)
			return result, nil
		}
		sug, err := oo.SuggestName(ctx, raw, sample, entry.ClusterLabel)
		if err != nil {
			return SetupResult{}, fail("look for this cluster's Pods in", err)
		}
		switch {
		case sug.Value == "":
			result.Outcome, result.Reason = SetupNotFound, sug.Reason
		case sug.UnsafeDefault || sug.Value == oo.UnsafeDefaultName:
			result.Outcome = SetupNotFound
			result.Reason = fmt.Sprintf("%s Radar won't use %s=%q: it is a default name other clusters share.", sug.Reason, sug.Label, sug.Value)
		case sug.Conflict:
			result.Outcome = SetupNotFound
			result.Reason = fmt.Sprintf("%s Radar won't use %s=%q: it doesn't identify this cluster alone.", sug.Reason, sug.Label, sug.Value)
		}
		if result.Outcome == SetupNotFound {
			if req.Confirm != nil {
				return SetupResult{}, setupErrorf("Radar won't use %s=%q for this cluster. %s", req.Confirm.Label, req.Confirm.Value, sug.Reason)
			}
			result.ClusterNames = listNames(ctx, raw, entry.ClusterLabel, conn.Token)
			return result, nil
		}
		suggestion := oo.LabelValue{Label: sug.Label, Value: sug.Value}
		if req.Confirm == nil {
			result.Outcome, result.Suggestion, result.Reason = SetupConfirm, &suggestion, sug.Reason
			return result, nil
		}
		if strings.ToLower(strings.TrimSpace(req.Confirm.Label)) != sug.Label || req.Confirm.Value != sug.Value {
			return SetupResult{}, setupErrorf("OpenObserve now suggests %s=%q for this cluster, not %s=%q. Test again and confirm the new name.", sug.Label, sug.Value, req.Confirm.Label, req.Confirm.Value)
		}
		m, err := oo.ConfirmedNameMatch(ctx, raw, sug.Label, sug.Value, uid)
		if err != nil {
			if isUnreachable(err) {
				return SetupResult{}, fail("check the name in", err)
			}
			return SetupResult{}, setupErrorf("Radar won't use %s=%q for this cluster: %s.", sug.Label, sug.Value, redact(trimPkgPrefix(err), conn.Token))
		}
		match = m
	}

	versionWarning := ""
	if legacy, err := oo.LegacyFilterBehaviour(ctx, raw); err == nil && legacy {
		versionWarning = oo.VersionWarning
	}
	stored := config.OpenObserveMatch{Label: match.Label, Value: match.Value, By: match.By, Context: deps.ContextName}
	if match.By == config.OpenObserveMatchByName {
		now := time.Now
		if deps.Now != nil {
			now = deps.Now
		}
		stored.ConfirmedAt = now().UTC().Format(time.RFC3339)
	}
	if deps.Save == nil {
		return SetupResult{}, errors.New("no way to save the OpenObserve connection")
	}
	if err := deps.Save(func(c *config.Config) {
		next := entry.Clone()
		if cur := c.OpenObserve; cur != nil && !resetMatches && cur.BaseURL == next.BaseURL && cur.Org == next.Org {
			// Matches saved for other clusters since this request started stay.
			next.Matches = maps.Clone(cur.Matches)
		}
		if next.Matches == nil {
			next.Matches = map[string]config.OpenObserveMatch{}
		}
		if prev, ok := next.Matches[uid]; ok && prev.By == config.OpenObserveMatchByName && stored.By == config.OpenObserveMatchByID {
			// The ID match is used first anyway; the user's confirmed name
			// stays in case the ID label disappears.
			prev.Context = stored.Context
			stored = prev
		}
		next.Matches[uid] = stored
		c.OpenObserve = &next
	}); err != nil {
		return SetupResult{}, err
	}
	result.Outcome = SetupSaved
	result.Match = &MatchView{Label: match.Label, Value: match.Value, By: match.By}
	result.Notes = SetupNotes{
		Collector:      match.Dialect == oo.DialectCollector,
		Missing:        familyNames(match.Missing),
		Warning:        match.Warning,
		VersionWarning: versionWarning,
	}
	return result, nil
}

// setupEntry builds the connection to test from the request and the saved
// one. A saved secret is reused only for the same address, organization and
// user. resetMatches is set when the matches belong to another organization's
// data.
func setupEntry(existing *config.OpenObserveConfig, req SetupRequest) (config.OpenObserveConfig, bool, error) {
	if req.UseSaved {
		if existing == nil {
			return config.OpenObserveConfig{}, false, setupErrorf("There is no saved OpenObserve connection. Enter the connection details.")
		}
		return existing.Clone(), false, nil
	}
	entry := config.OpenObserveConfig{
		BaseURL:      strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		Org:          strings.TrimSpace(req.Org),
		User:         strings.TrimSpace(req.User),
		CACertFile:   strings.TrimSpace(req.CACertFile),
		ClusterLabel: strings.ToLower(strings.TrimSpace(req.ClusterLabel)),
	}
	if entry.Org == "" {
		entry.Org = "default"
	}
	switch {
	case entry.BaseURL == "":
		return entry, false, setupErrorf("Enter the OpenObserve address.")
	case entry.User == "":
		return entry, false, setupErrorf("Enter the user or service account email.")
	case entry.ClusterLabel != "" && (!labelNamePattern.MatchString(entry.ClusterLabel) || strings.HasPrefix(entry.ClusterLabel, "__")):
		return entry, false, setupErrorf("The cluster label %q isn't a valid label name. Use letters, digits and underscores.", entry.ClusterLabel)
	}
	sameTarget := existing != nil && existing.BaseURL == entry.BaseURL && existing.Org == entry.Org && existing.User == entry.User
	switch {
	case req.Token != "":
		entry.Token = req.Token
	case sameTarget:
		entry.Token, entry.TokenEnv = existing.Token, existing.TokenEnv
	default:
		// Never send a saved secret to a different server or account.
		return entry, false, setupErrorf("Enter the token.")
	}
	reset := existing == nil || existing.BaseURL != entry.BaseURL || existing.Org != entry.Org
	if !reset {
		entry.Matches = maps.Clone(existing.Matches)
	}
	return entry, reset, nil
}

func listNames(ctx context.Context, raw prom.Transport, overrideLabel, token string) []oo.LabelValue {
	names, err := oo.ListClusterNames(ctx, raw, overrideLabel)
	if err != nil {
		log.Printf("[openobserve] Listing the cluster names OpenObserve has: %s", redact(err.Error(), token))
		return nil
	}
	return names
}

// connectionProblem turns a connection validation error into a sentence.
func connectionProblem(err error) string {
	msg := strings.TrimPrefix(err.Error(), "openobserve: ")
	msg = strings.ReplaceAll(msg, "base URL", "address")
	return sentence(prom.RedactURLs(msg))
}

// describeFailure explains why a request to OpenObserve failed. what is the
// action, as in "Radar couldn't <what> OpenObserve"; empty for the login check.
func describeFailure(what string, conn oo.Connection, err error) string {
	address := prom.SafeAddress(conn.BaseURL)
	var httpErr *prom.HTTPError
	var limited oo.ErrRateLimited
	switch {
	case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden && what != "":
		return "The login works, but OpenObserve doesn't let this account read metrics. Give it a role that can read the metrics streams, then try again."
	case errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden):
		return "OpenObserve rejected the login. Check the user and token."
	case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound:
		return fmt.Sprintf("OpenObserve at %s has no Prometheus API for organization %q. Check the address and organization.", address, conn.Org)
	case errors.As(err, &limited):
		return "OpenObserve is rate limiting Radar's requests. Try again in a minute."
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return fmt.Sprintf("OpenObserve at %s didn't answer in time. Try again, or check that it is up.", address)
	case errors.Is(err, context.Canceled):
		return "The check was canceled."
	case errors.As(err, &httpErr):
		detail := shortBody(httpErr.Body, conn.Token)
		if what == "" {
			what = "check the login with"
		}
		if detail != "" {
			return fmt.Sprintf("Radar couldn't %s OpenObserve: it answered HTTP %d (%s).", what, httpErr.StatusCode, detail)
		}
		return fmt.Sprintf("Radar couldn't %s OpenObserve: it answered HTTP %d.", what, httpErr.StatusCode)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Sprintf("Radar couldn't reach OpenObserve at %s: %s.", address, redact(trimSentence(urlErr.Err.Error()), conn.Token))
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return fmt.Sprintf("Radar couldn't reach OpenObserve at %s: %s.", address, redact(trimSentence(netErr.Error()), conn.Token))
	}
	if what == "" {
		what = "check the login with"
	}
	return fmt.Sprintf("Radar couldn't %s OpenObserve: %s.", what, redact(trimSentence(trimPkgPrefix(err)), conn.Token))
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

const maxDetailRunes = 200

func shortBody(body []byte, token string) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(string(body), ""))
	s = strings.Join(strings.Fields(s), " ")
	s = redact(s, token)
	if utf8.RuneCountInString(s) > maxDetailRunes {
		s = string([]rune(s)[:maxDetailRunes]) + "…"
	}
	return trimSentence(s)
}

func redact(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "<redacted>")
	}
	return prom.RedactURLs(s)
}

func trimSentence(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ".")
}

func sentence(s string) string {
	s = trimSentence(s)
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:] + "."
}
