// Package telemetry implements Radar's opt-in usage reports.
//
// Nothing is recorded or sent until the user says yes. A yes starts a daily
// report under a random install ID: which views and actions were used, MCP
// tool calls, Radar's own setup, and each connected cluster's version,
// platform and size ranges. It never carries names (resources, namespaces,
// clusters, hosts) or contents. The pending report lives in
// ~/.radar/usage-report.json so the user can read exactly what will be sent.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/internal/version"
)

const (
	// Endpoint receives usage reports. It is Skyhook's own relay, the same
	// host as the update check, so no analytics vendor SDK ships in Radar.
	Endpoint = "https://releases.skyhook.io/radar/usage"

	reportInterval = 24 * time.Hour
	// A cluster's shape is re-read at most this often; it barely moves
	// within a day and reading it walks the whole Pod cache.
	clusterSampleInterval = 15 * time.Minute
	// A machine whose ~/.radar is younger than this counts as a fresh
	// install for the one-time prompt. Long enough to survive a first
	// session that ended before the prompt appeared.
	freshInstallWindow = 7 * 24 * time.Hour
	retryDelay         = 24 * time.Hour
	tickInterval       = time.Minute
	schemaVersion      = 2
)

// State is the effective usage-data decision.
type State string

const (
	StateUndecided State = "undecided"
	StateOn        State = "on"
	StateOff       State = "off"
	// StateLog records like StateOn but prints each report to the log
	// instead of sending it (RADAR_TELEMETRY=log).
	StateLog State = "log"
)

// Source names the control that decided State, so Settings can say which one
// to change.
type Source string

const (
	SourceDefault    Source = "default"
	SourceUser       Source = "user"
	SourceEnv        Source = "env"
	SourceDoNotTrack Source = "do-not-track"
	// SourceDeployment covers installs whose host manages usage data
	// itself (Radar Cloud). Shared in-cluster installs are not this: any
	// team member may opt them in.
	SourceDeployment Source = "deployment"
)

// Status is what GET /api/telemetry returns.
type Status struct {
	State            State      `json:"state"`
	Source           Source     `json:"source"`
	CanChange        bool       `json:"canChange"`
	Endpoint         string     `json:"endpoint"`
	DevelopmentBuild bool       `json:"developmentBuild"`
	NextReportAt     *time.Time `json:"nextReportAt,omitempty"`
	LastSentAt       *time.Time `json:"lastSentAt,omitempty"`
	Preview          Report     `json:"preview"`
	// FirstRunPrompt asks the UI to show the one-time prompt for a fresh
	// install. Upgrading installs are asked in What's New instead.
	FirstRunPrompt bool `json:"firstRunPrompt"`
	// Shared means the choice covers everyone using this Radar.
	Shared bool `json:"shared"`
	// DecidedBy and DecidedAt say who answered, for shared installs with
	// sign-in. Shown in Settings only; never part of a report.
	DecidedBy string     `json:"decidedBy,omitempty"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
	// ChoiceStorage is where the choice is kept: "local" (settings.json),
	// "cluster" (the chart's ConfigMap) or "pod" (a shared install that
	// could not use the ConfigMap, so a restart resets the choice).
	ChoiceStorage string `json:"choiceStorage"`
}

// Recording reports whether State counts usage.
func (s State) Recording() bool { return s == StateOn || s == StateLog }

// Options wires the collector to facts only the server knows.
type Options struct {
	// Hosted reports an install whose host manages usage data itself (Radar
	// Cloud). It is always off and nobody in the UI can change that.
	Hosted func() bool
	// Shared reports an install several people use (in-cluster, or behind
	// sign-in). Anyone on the team may opt it in; the choice covers all.
	Shared func() bool
	// Mode is "local", "desktop" or "in-cluster".
	Mode func() string
	// ClusterSample describes the cluster Radar is connected to right now.
	// key identifies it locally so a day's clusters can be told apart; it is
	// hashed before it is stored and never sent.
	ClusterSample func() (key string, shape ClusterShape, ok bool)
	// Setup describes how this Radar is configured.
	Setup func() Setup
	// Contexts is how many kubeconfig contexts Radar can see.
	Contexts func() int
	// StorageScope names where Load and Save keep the choice; see
	// Status.ChoiceStorage. Defaults to "local".
	StorageScope func() string
	// Load and Save replace settings.json as the home of the choice, the
	// prompt-shown flag and the identity. In-cluster that is a ConfigMap, so
	// they survive restarts; the daily counts stay local either way.
	Load func() (settings.Settings, error)
	Save func(func(*settings.Settings)) error
	// InstalledAt is when Radar first ran on this machine (Unix seconds, 0
	// if unknown). It decides the first-install prompt and is never sent.
	InstalledAt func() int64
}

// ErrManaged means the decision is fixed by an environment variable or the
// deployment, so the UI cannot change it.
var ErrManaged = errors.New("usage data is managed by this installation's configuration")

// DoNotTrack reports whether the DO_NOT_TRACK convention asks Radar to make
// no non-essential requests.
func DoNotTrack() bool { return doNotTrack(os.Getenv) }

func doNotTrack(env func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(env("DO_NOT_TRACK"))) {
	case "", "0", "false", "no":
		return false
	}
	return true
}

// resolve is the whole precedence chain, kept pure for tests:
// DO_NOT_TRACK, then RADAR_TELEMETRY, then hosted installs (off), then the
// user's saved answer, then undecided.
func resolve(env func(string) string, hosted bool, choice *settings.UsageDataChoice) (State, Source) {
	if doNotTrack(env) {
		return StateOff, SourceDoNotTrack
	}
	// Radar Cloud runs its own program; not even an environment variable
	// turns the OSS reports on under it.
	if hosted {
		return StateOff, SourceDeployment
	}
	switch v := strings.ToLower(strings.TrimSpace(env("RADAR_TELEMETRY"))); v {
	case "":
	case "on", "1", "true", "yes":
		return StateOn, SourceEnv
	case "log":
		return StateLog, SourceEnv
	default:
		// "off", and anything unrecognized: a typo meant to turn usage
		// data off must not leave it on.
		return StateOff, SourceEnv
	}
	if choice == nil {
		return StateUndecided, SourceDefault
	}
	if choice.Enabled {
		return StateOn, SourceUser
	}
	return StateOff, SourceUser
}

func invalidTelemetryEnv(env func(string) string) (string, bool) {
	switch v := strings.ToLower(strings.TrimSpace(env("RADAR_TELEMETRY"))); v {
	case "", "on", "1", "true", "yes", "log", "off", "0", "false", "no":
		return "", false
	default:
		return v, true
	}
}

// Collector accumulates the pending report and sends it daily.
type Collector struct {
	opts Options
	env  func(string) string
	now  func() time.Time
	path string
	send func(context.Context, Report) error
	load func() (settings.Settings, error)
	save func(func(*settings.Settings)) error

	mu          sync.Mutex
	state       State
	source      Source
	p           pending
	dirty       bool
	nextAttempt time.Time
	// gen changes whenever the pending report is replaced by a decision, so a
	// send that started before an opt-out cannot commit afterwards.
	gen         uint64
	cancelSend  context.CancelFunc
	promptShown bool
	sampledAt   map[string]time.Time
	choice      *settings.UsageDataChoice
	identity    *settings.UsageIdentity
	// decisionGen changes on every SetChoice, so a background refresh
	// that read the store before the decision can't commit stale state.
	decisionGen    uint64
	lastLoadErrLog time.Time
}

var (
	defaultMu sync.Mutex
	defaultC  *Collector
)

func current() *Collector {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultC
}

// Start creates the process-wide collector and runs its daily loop until ctx
// ends.
func Start(ctx context.Context, opts Options) *Collector {
	c := newCollector(opts)
	defaultMu.Lock()
	defaultC = c
	defaultMu.Unlock()
	if v, bad := invalidTelemetryEnv(c.env); bad {
		log.Printf("[telemetry] RADAR_TELEMETRY=%q is not on, off or log; treating it as off", v)
	}
	c.refresh()
	log.Printf("[telemetry] Usage data: %s (%s)", c.state, describeSource(c.source))
	go c.run(ctx)
	return c
}

func newCollector(opts Options) *Collector {
	if opts.Hosted == nil {
		opts.Hosted = func() bool { return false }
	}
	if opts.Shared == nil {
		opts.Shared = func() bool { return false }
	}
	if opts.Mode == nil {
		opts.Mode = func() string { return "local" }
	}
	if opts.ClusterSample == nil {
		opts.ClusterSample = func() (string, ClusterShape, bool) { return "", ClusterShape{}, false }
	}
	if opts.Setup == nil {
		opts.Setup = func() Setup { return Setup{} }
	}
	if opts.StorageScope == nil {
		opts.StorageScope = func() string { return "local" }
	}
	if opts.Contexts == nil {
		opts.Contexts = func() int { return 0 }
	}
	if opts.InstalledAt == nil {
		opts.InstalledAt = func() int64 { return 0 }
	}
	c := &Collector{
		opts: opts,
		env:  os.Getenv,
		now:  time.Now,
		path: pendingPath(),
		send: postReport,
		load: settings.LoadChecked,
		// Checked: a consent write must never replace an unreadable
		// settings file, and the user's other preferences with it.
		save: func(fn func(*settings.Settings)) error {
			_, err := settings.UpdateChecked(fn)
			return err
		},
	}
	if opts.Load != nil && opts.Save != nil {
		c.load, c.save = opts.Load, opts.Save
	}
	c.p = c.readPending()
	return c
}

func pendingPath() string {
	p := settings.Path()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "usage-report.json")
}

func describeSource(s Source) string {
	switch s {
	case SourceDoNotTrack:
		return "DO_NOT_TRACK is set"
	case SourceEnv:
		return "set by RADAR_TELEMETRY"
	case SourceDeployment:
		return "managed by Radar Cloud"
	case SourceUser:
		return "your choice in Settings"
	default:
		return "nothing is sent unless you opt in"
	}
}

// refresh re-reads the decision. Env vars never change at runtime; the saved
// answer can change through Settings or a hand edit of settings.json.
func (c *Collector) refresh() {
	c.mu.Lock()
	gen := c.decisionGen
	c.mu.Unlock()

	hosted := c.opts.Hosted()
	s, err := c.load()
	var choice *settings.UsageDataChoice
	if err == nil {
		choice = s.UsageData
	}
	state, source := resolve(c.env, hosted, choice)
	// A failed read only matters when the answer comes from the store. Keep
	// the last known state rather than falling back to "undecided", which
	// would drop the day's counts and could show the first-install prompt.
	if err != nil && (source == SourceDefault || source == SourceUser) {
		c.logLoadError(err)
		c.mu.Lock()
		if c.state == "" {
			c.state, c.source = StateOff, SourceDefault
		}
		c.mu.Unlock()
		return
	}

	// The identity exists exactly while recording. The save re-decides from
	// what is stored at that moment, so a read that raced an opt-out can't
	// bring an identity back, and two replicas settle on one identity.
	if err == nil {
		recordingIn := func(st *settings.Settings) bool {
			sState, _ := resolve(c.env, hosted, st.UsageData)
			return sState.Recording()
		}
		switch {
		case state.Recording() && s.UsageIdentity == nil:
			id := newIdentity()
			if c.save(func(st *settings.Settings) {
				if st.UsageIdentity == nil && recordingIn(st) {
					st.UsageIdentity = id
				}
			}) == nil {
				if again, err2 := c.load(); err2 == nil {
					s = again
				}
			}
		case !state.Recording() && s.UsageIdentity != nil:
			if c.save(func(st *settings.Settings) {
				if !recordingIn(st) {
					st.UsageIdentity = nil
				}
			}) == nil {
				s.UsageIdentity = nil
			}
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// A decision made while this read was in flight is newer; don't
	// overwrite it with what was read before it.
	if c.decisionGen != gen {
		return
	}
	c.state, c.source = state, source
	if err == nil {
		c.promptShown = s.UsagePromptShownAt != nil
		c.choice = s.UsageData
		c.identity = s.UsageIdentity
	}
	if !state.Recording() {
		c.discardLocked()
		return
	}
	if c.p.PeriodStart.IsZero() {
		c.p.PeriodStart = c.now()
		c.dirty = true
	}
}

func (c *Collector) logLoadError(err error) {
	c.mu.Lock()
	now := c.now()
	quiet := now.Sub(c.lastLoadErrLog) < 10*time.Minute
	if !quiet {
		c.lastLoadErrLog = now
	}
	c.mu.Unlock()
	if !quiet {
		log.Printf("[telemetry] Could not read the usage-data choice, keeping the last known state: %v", err)
	}
}

// discardLocked drops everything recorded so far and aborts a send in
// flight. Turning usage data off must leave nothing behind that a later yes,
// or a request already on the wire, could send.
func (c *Collector) discardLocked() {
	c.abortSendLocked()
	c.p = pending{}
	c.sampledAt = nil
	c.dirty = false
	if c.path != "" {
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			log.Printf("[telemetry] Failed to remove %s: %v", c.path, err)
		}
	}
}

func (c *Collector) abortSendLocked() {
	c.gen++
	if c.cancelSend != nil {
		c.cancelSend()
		c.cancelSend = nil
	}
}

// CurrentStatus returns the current decision plus a preview of the next report.
func CurrentStatus() Status {
	if c := current(); c != nil {
		return c.Status()
	}
	return Status{State: StateOff, Source: SourceDefault, Endpoint: Endpoint}
}

// Status returns the current decision plus a preview of the next report.
func (c *Collector) Status() Status {
	c.mu.Lock()
	st := Status{
		State:            c.state,
		Source:           c.source,
		CanChange:        c.source == SourceDefault || c.source == SourceUser,
		Endpoint:         Endpoint,
		DevelopmentBuild: version.IsDevelopmentBuild(),
		LastSentAt:       c.p.LastSentAt,
	}
	if c.state.Recording() && !c.p.PeriodStart.IsZero() {
		next := c.p.PeriodStart.Add(reportInterval)
		st.NextReportAt = &next
	}
	st.FirstRunPrompt = c.state == StateUndecided && st.CanChange && !c.promptShown &&
		freshInstall(c.opts.InstalledAt(), c.now())
	st.Shared = c.opts.Shared()
	st.ChoiceStorage = c.opts.StorageScope()
	if c.choice != nil && (c.source == SourceUser) {
		at := c.choice.DecidedAt
		st.DecidedAt = &at
		st.DecidedBy = c.choice.DecidedBy
	}
	p := c.p.clone()
	identity := c.identity
	c.mu.Unlock()
	// Before anything is recorded, preview the cluster Radar is on now, so
	// the example shows exactly what a report would carry.
	if len(p.Clusters) == 0 {
		if key, shape, ok := c.opts.ClusterSample(); ok {
			p.Clusters = map[string]ClusterShape{hashKey(key): shape}
		}
	}
	st.Preview = c.buildReport(p, identity)
	return st
}

func freshInstall(installedAt int64, now time.Time) bool {
	if installedAt <= 0 {
		return false
	}
	return now.Sub(time.Unix(installedAt, 0)) < freshInstallWindow
}

// MarkPromptShown records that the first-install prompt has been shown on
// this machine so it never appears again.
func MarkPromptShown() error {
	c := current()
	if c == nil {
		return errors.New("usage data is not initialized")
	}
	now := c.now()
	if err := c.save(func(s *settings.Settings) { s.UsagePromptShownAt = &now }); err != nil {
		return fmt.Errorf("save prompt shown: %w", err)
	}
	c.mu.Lock()
	c.promptShown = true
	c.mu.Unlock()
	return nil
}

// SetChoice saves the user's answer. A yes starts a fresh period; a no deletes
// whatever was pending.
func SetChoice(enabled bool, by string) (Status, error) {
	c := current()
	if c == nil {
		return Status{}, errors.New("usage data is not initialized")
	}
	return c.SetChoice(enabled, by)
}

// SetChoice saves the user's answer; see the package-level SetChoice.
func (c *Collector) SetChoice(enabled bool, by string) (Status, error) {
	c.mu.Lock()
	canChange := c.source == SourceDefault || c.source == SourceUser
	if canChange {
		c.decisionGen++
	}
	c.mu.Unlock()
	if !canChange {
		return c.Status(), ErrManaged
	}
	now := c.now()
	if err := c.save(func(s *settings.Settings) {
		s.UsageData = &settings.UsageDataChoice{Enabled: enabled, DecidedAt: now, DecidedBy: by}
		// Every opt-in is a new install as far as reports go.
		s.UsageIdentity = nil
		if enabled {
			s.UsageIdentity = newIdentity()
		}
	}); err != nil {
		return c.Status(), fmt.Errorf("save usage-data choice: %w", err)
	}
	if enabled {
		c.mu.Lock()
		c.abortSendLocked()
		c.p = pending{PeriodStart: now}
		c.sampledAt = nil
		c.dirty = true
		c.mu.Unlock()
	}
	c.refresh()
	c.flush()
	return c.Status(), nil
}

func (c *Collector) run(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.flush()
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

func (c *Collector) tick(ctx context.Context) {
	c.refresh()
	c.sampleCluster()
	c.flush()
	c.maybeSend(ctx)
}

func (c *Collector) sampleCluster() {
	c.mu.Lock()
	recording := c.state.Recording()
	c.mu.Unlock()
	if !recording {
		return
	}
	key, shape, ok := c.opts.ClusterSample()
	if !ok {
		return
	}
	hashed := hashKey(key)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Recording() || now.Sub(c.sampledAt[hashed]) < clusterSampleInterval {
		return
	}
	if c.sampledAt == nil {
		c.sampledAt = map[string]time.Time{}
	}
	c.sampledAt[hashed] = now
	if c.p.Clusters == nil {
		c.p.Clusters = map[string]ClusterShape{}
	}
	c.p.Clusters[hashed] = shape
	c.dirty = true
}

// maybeSend sends the day's report. It takes the counts out before the
// request so recording continues into the next day, and puts them back if
// the request fails. A decision made while a request is in flight (the gen
// check) wins over the request's outcome.
func (c *Collector) maybeSend(ctx context.Context) {
	c.mu.Lock()
	now := c.now()
	due := c.state.Recording() && !c.p.PeriodStart.IsZero() &&
		now.Sub(c.p.PeriodStart) >= reportInterval && now.After(c.nextAttempt)
	if !due {
		c.mu.Unlock()
		return
	}
	identity := c.identity
	// No stored identity (its save failed) means no report: an empty
	// install ID would make the day unattributable, and a throwaway salt
	// would scramble the cluster IDs. Keep the counts and try later.
	if identity == nil || identity.InstallID == "" || identity.ClusterSalt == "" {
		c.nextAttempt = now.Add(time.Hour)
		c.mu.Unlock()
		log.Printf("[telemetry] Usage report held: no saved install identity yet")
		return
	}
	state := c.state
	taken := c.p
	c.p = pending{PeriodStart: now, LastSentAt: taken.LastSentAt}
	c.dirty = true
	gen := c.gen
	sendCtx, cancel := context.WithCancel(ctx)
	c.cancelSend = cancel
	c.mu.Unlock()
	defer cancel()

	// A day with no recorded use is not a report: the UI was never opened.
	if !taken.empty() {
		report := c.buildReport(taken, identity)
		switch {
		case state == StateLog:
			logReport(report, "RADAR_TELEMETRY=log")
		case version.IsDevelopmentBuild():
			logReport(report, "development build")
		default:
			if err := c.send(sendCtx, report); err != nil {
				c.mu.Lock()
				if c.gen == gen {
					log.Printf("[telemetry] Usage report not sent, retrying in a day: %v", err)
					c.nextAttempt = now.Add(retryDelay)
					c.p.mergeFrom(taken)
					c.p.PeriodStart = taken.PeriodStart
				}
				c.mu.Unlock()
				return
			}
		}
	}

	c.mu.Lock()
	if c.gen == gen {
		sent := now
		c.p.LastSentAt = &sent
		c.cancelSend = nil
	}
	c.mu.Unlock()
	c.flush()
}

func logReport(r Report, why string) {
	body, _ := json.MarshalIndent(r, "", "  ")
	log.Printf("[telemetry] Usage report not sent (%s):\n%s", why, body)
}

func postReport(ctx context.Context, r Report) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// The report's own version: custom build versions are masked there and
	// must not come back through the header.
	req.Header.Set("User-Agent", "radar/"+r.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("usage endpoint returned %d", resp.StatusCode)
	}
	return nil
}

func (c *Collector) readPending() pending {
	if c.path == "" {
		return pending{}
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return pending{}
	}
	var p pending
	if err := json.Unmarshal(data, &p); err != nil {
		log.Printf("[telemetry] Ignoring unreadable %s: %v", c.path, err)
		return pending{}
	}
	return p
}

// flush writes the pending report while holding the lock. The file is
// small, and writing outside the lock let a write that started before an
// opt-out recreate the file the opt-out had just deleted.
func (c *Collector) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty || c.path == "" || !c.state.Recording() {
		return
	}
	data, err := json.MarshalIndent(c.p, "", "  ")
	if err != nil {
		return
	}
	if err := writeAtomic(c.path, data); err != nil {
		log.Printf("[telemetry] Failed to save pending report: %v", err)
		return
	}
	c.dirty = false
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Flush saves the pending report now. Called on shutdown so counts from the
// last minute survive a restart.
func Flush() {
	if c := current(); c != nil {
		c.flush()
	}
}
