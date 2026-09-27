// Package usagedata implements Radar's opt-in usage reports.
//
// Nothing is recorded or sent until the user says yes. A yes starts a daily
// anonymous report: which views and actions were used, MCP tool calls,
// Radar's own setup, and each connected cluster's minor version, platform,
// node-count range and known integrations. It carries no install ID and never
// carries names (resources, namespaces, clusters, hosts) or contents. The
// pending report lives in ~/.radar/usage-report.json so the user can read
// exactly what will be sent.
package usagedata

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
	// Endpoint receives usage reports: Skyhook's own receiver, so no analytics
	// vendor SDK ships in Radar. It has a host of its own so an operator can
	// block usage reports at the firewall and still get update checks.
	Endpoint = "https://usage.radarhq.io/report"

	reportInterval = 24 * time.Hour
	// A cluster's shape is re-read at most this often; it barely moves
	// within a day and reading it walks the whole Pod cache.
	clusterSampleInterval = 15 * time.Minute
	// A machine whose ~/.radar is younger than this counts as a fresh
	// install for the one-time prompt. Long enough to survive a first
	// session that ended before the prompt appeared.
	freshInstallWindow = 7 * 24 * time.Hour
	// Someone who closed the question without answering may be asked again
	// after this long. An explicit "no" is never asked again.
	reaskInterval = 90 * 24 * time.Hour
	retryDelay    = 24 * time.Hour
	tickInterval  = time.Minute
	// Bounds the report sent as a shared install shuts down, so a slow or
	// unreachable endpoint can't hold up the pod's termination.
	shutdownSendTimeout = 5 * time.Second
	schemaVersion       = 3
)

// State is the effective usage-data decision.
type State string

const (
	StateUndecided State = "undecided"
	StateOn        State = "on"
	StateOff       State = "off"
)

// Source names the control that decided State, so Settings can say which one
// to change.
type Source string

const (
	SourceDefault Source = "default"
	SourceUser    Source = "user"
	SourceEnv     Source = "env"
	// SourceDeployment covers installs whose host manages usage data
	// itself (Radar Cloud).
	SourceDeployment Source = "deployment"
)

// Status is what GET /api/usage-data returns.
type Status struct {
	State            State      `json:"state"`
	Source           Source     `json:"source"`
	CanChange        bool       `json:"canChange"`
	DevelopmentBuild bool       `json:"developmentBuild"`
	NextReportAt     *time.Time `json:"nextReportAt,omitempty"`
	Preview          Report     `json:"preview"`
	// FirstRunPrompt asks the UI to show the one-time prompt for a fresh
	// install. Upgrading installs are asked in What's New instead.
	FirstRunPrompt bool `json:"firstRunPrompt"`
	// Ask lets What's New ask an undecided user: never asked, or asked and
	// left unanswered long enough ago.
	Ask bool `json:"ask"`
	// Shared means several people use this Radar, so its configuration
	// decides (the Helm value, or RADAR_USAGE_REPORTING) and nobody is asked.
	Shared bool `json:"shared"`
}

// Recording reports whether State counts usage.
func (s State) Recording() bool { return s == StateOn }

// Options wires the collector to facts only the server knows.
type Options struct {
	// Hosted reports an install whose host manages usage data itself (Radar
	// Cloud). It is always off and nobody in the UI can change that.
	Hosted func() bool
	// Shared reports an install several people use (in-cluster, behind
	// sign-in, or on a listener others can reach). Its reports describe how
	// the team uses it, so only its configuration decides: nobody is asked,
	// and a choice saved in settings.json is ignored.
	Shared func() bool
	// Mode is "local", "desktop", "in-cluster" or "cloud".
	Mode func() string
	// ClusterSample describes the cluster Radar is connected to right now.
	// key identifies it locally so a day's clusters can be told apart; it is
	// hashed before it is stored and never sent.
	ClusterSample func() (key string, shape ClusterShape, ok bool)
	// Setup describes how this Radar is configured.
	Setup func() Setup
	// Contexts is how many kubeconfig contexts Radar can see.
	Contexts func() int
	// SendOnShutdown sends whatever is pending when Radar stops, however
	// short the period. In-cluster the pending report lives on an emptyDir
	// that dies with the pod, so without it a pod replaced more often than
	// daily would never report.
	SendOnShutdown bool
	// InstalledAt is when Radar first ran on this machine (Unix seconds, 0
	// if unknown). It decides the first-install prompt and is never sent.
	InstalledAt func() int64
}

// ErrManaged means the decision is fixed by an environment variable or the
// deployment, so the UI cannot change it.
var ErrManaged = errors.New("usage data is managed by this installation's configuration")

// errReportRefused means the receiver looked at the report and won't take
// it (400 or 413). Sending the same counts again would be refused again, so
// they are dropped rather than kept and retried every day. Any other status,
// such as a 404 from a host not serving the receiver yet, is retried.
var errReportRefused = errors.New("usage report refused")

// doNotTrack reports the DO_NOT_TRACK convention. It never overrides a choice:
// on a laptop the person who says yes is the person whose usage it is, and a
// shared Radar is decided by its operator's Helm value. It only keeps Radar
// from asking a second time.
func doNotTrack(env func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(env("DO_NOT_TRACK"))) {
	case "", "0", "false", "no":
		return false
	}
	return true
}

// resolve is the whole precedence chain, kept pure for tests: hosted installs
// (off), then RADAR_USAGE_REPORTING, then the saved answer, then undecided.
func resolve(env func(string) string, hosted bool, choice *settings.UsageDataChoice) (State, Source) {
	// Radar Cloud runs its own program; not even an environment variable
	// turns the OSS reports on under it.
	if hosted {
		return StateOff, SourceDeployment
	}
	if set, on, _ := usageReportingEnv(env); set {
		if on {
			return StateOn, SourceEnv
		}
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

// usageReportingEnv reads RADAR_USAGE_REPORTING, which the chart sets from
// usageReporting.enabled. Anything unrecognized means off: a typo meant to
// turn it off must not leave it on.
func usageReportingEnv(env func(string) string) (set, on, valid bool) {
	switch strings.ToLower(strings.TrimSpace(env("RADAR_USAGE_REPORTING"))) {
	case "":
		return false, false, true
	case "on", "1", "true", "yes":
		return true, true, true
	case "off", "0", "false", "no":
		return true, false, true
	default:
		return true, false, false
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

	mu sync.Mutex
	// sendMu keeps sends one at a time, so the send at shutdown waits for a
	// daily send in flight and includes its counts if that send was cancelled.
	sendMu sync.Mutex
	// decideMu runs decisions and refreshes one at a time, from reading or
	// saving the choice through applying it, so neither can put back an older
	// answer.
	decideMu    sync.Mutex
	state       State
	source      Source
	p           pending
	dirty       bool
	nextAttempt time.Time
	// gen changes whenever the pending report is replaced by a decision, so a
	// send that started before an opt-out cannot commit afterwards.
	gen            uint64
	cancelSend     context.CancelFunc
	promptShownAt  *time.Time
	sampledAt      time.Time
	lastLoadErrLog time.Time
	// The Settings preview's cluster while nothing is recorded, cached like a
	// recorded sample: reading it walks the caches and API discovery.
	previewShape ClusterShape
	previewAt    time.Time
	// previewGen changes on a context switch, so a sample of the previous
	// cluster still in flight is not cached afterwards.
	previewGen uint64
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
	if _, _, valid := usageReportingEnv(c.env); !valid {
		log.Printf("[usage] RADAR_USAGE_REPORTING=%q is not on or off; treating it as off", c.env("RADAR_USAGE_REPORTING"))
	}
	c.refresh()
	log.Printf("[usage] Usage data: %s (%s)", c.state, describeSource(c.source))
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
	case SourceEnv:
		return "set by RADAR_USAGE_REPORTING"
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
	// A decision saves and applies under decideMu. Reading the store in
	// the middle of one would see the old answer and could put it back.
	c.decideMu.Lock()
	defer c.decideMu.Unlock()

	hosted := c.opts.Hosted()
	shared := c.opts.Shared()
	s, err := c.load()
	var choice *settings.UsageDataChoice
	if err == nil && !shared {
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

	c.mu.Lock()
	defer c.mu.Unlock()
	c.state, c.source = state, source
	if err == nil {
		c.promptShownAt = s.UsagePromptShownAt
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
		log.Printf("[usage] Could not read the usage-data choice, keeping the last known state: %v", err)
	}
}

// discardLocked drops everything recorded so far and aborts a send in
// flight. Turning usage data off must leave nothing behind that a later yes,
// or a request already on the wire, could send.
func (c *Collector) discardLocked() {
	c.abortSendLocked()
	c.p = pending{}
	c.sampledAt = time.Time{}
	c.dirty = false
	if c.path != "" {
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			log.Printf("[usage] Failed to remove %s: %v", c.path, err)
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
	return Status{State: StateOff, Source: SourceDefault}
}

// Status returns the current decision plus a preview of the next report.
func (c *Collector) Status() Status {
	// These can reach the Kubernetes API, so never under c.mu: every
	// recorded API request takes it.
	installedAt := c.opts.InstalledAt()
	shared := c.opts.Shared()
	c.mu.Lock()
	st := Status{
		State:            c.state,
		Source:           c.source,
		CanChange:        !shared && (c.source == SourceDefault || c.source == SourceUser),
		DevelopmentBuild: version.IsDevelopmentBuild(),
	}
	if c.state.Recording() && !c.p.PeriodStart.IsZero() {
		next := c.p.PeriodStart.Add(reportInterval)
		st.NextReportAt = &next
	}
	now := c.now()
	undecided := c.state == StateUndecided && st.CanChange
	st.FirstRunPrompt = undecided && c.promptShownAt == nil && freshInstall(installedAt, now)
	// Someone who set DO_NOT_TRACK still gets the one question, but not again.
	st.Ask = undecided && (c.promptShownAt == nil || (!doNotTrack(c.env) && now.Sub(*c.promptShownAt) >= reaskInterval))
	st.Shared = shared
	p := c.p.clone()
	c.mu.Unlock()
	// Before anything is recorded, preview the cluster Radar is on now, so
	// the example shows exactly what a report would carry.
	if len(p.Clusters) == 0 {
		if shape, ok := c.previewCluster(); ok {
			p.Clusters = map[string]ClusterShape{"preview": shape}
		}
	}
	st.Preview = c.buildReport(p)
	return st
}

func (c *Collector) previewCluster() (ClusterShape, bool) {
	c.mu.Lock()
	if !c.previewAt.IsZero() && c.now().Sub(c.previewAt) < clusterSampleInterval {
		shape := c.previewShape
		c.mu.Unlock()
		return shape, true
	}
	gen := c.previewGen
	c.mu.Unlock()
	_, shape, ok := c.opts.ClusterSample()
	if !ok {
		return ClusterShape{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.previewGen != gen {
		return ClusterShape{}, false
	}
	c.previewShape, c.previewAt = shape, c.now()
	return shape, true
}

// ContextSwitched drops the cached preview cluster, so Settings never shows
// the previous cluster as the one to be sent, and lets the next tick sample
// the new cluster without waiting out the sample interval.
func ContextSwitched() {
	if c := current(); c != nil {
		c.mu.Lock()
		c.previewAt = time.Time{}
		c.sampledAt = time.Time{}
		c.previewGen++
		c.mu.Unlock()
	}
}

func freshInstall(installedAt int64, now time.Time) bool {
	if installedAt <= 0 {
		return false
	}
	return now.Sub(time.Unix(installedAt, 0)) < freshInstallWindow
}

// MarkPromptShown records that the question was just shown, answered or not.
// The first-install prompt then never appears again, and What's New waits
// reaskInterval before asking again.
func MarkPromptShown() error {
	c := current()
	if c == nil {
		return errors.New("usage data is not initialized")
	}
	// Under decideMu, so a refresh that read the settings before this save
	// can't apply its older, unset timestamp afterwards.
	c.decideMu.Lock()
	defer c.decideMu.Unlock()
	now := c.now()
	if err := c.save(func(s *settings.Settings) { s.UsagePromptShownAt = &now }); err != nil {
		return fmt.Errorf("save prompt shown: %w", err)
	}
	c.mu.Lock()
	c.promptShownAt = &now
	c.mu.Unlock()
	return nil
}

// SetChoice saves the user's answer. A yes starts a fresh period; a no deletes
// whatever was pending.
func SetChoice(enabled bool) (Status, error) {
	c := current()
	if c == nil {
		return Status{}, errors.New("usage data is not initialized")
	}
	return c.SetChoice(enabled)
}

// SetChoice saves the user's answer; see the package-level SetChoice.
func (c *Collector) SetChoice(enabled bool) (Status, error) {
	c.decideMu.Lock()
	defer c.decideMu.Unlock()
	hosted := c.opts.Hosted()
	shared := c.opts.Shared()
	c.mu.Lock()
	if shared || (c.source != SourceDefault && c.source != SourceUser) {
		c.mu.Unlock()
		return c.Status(), ErrManaged
	}
	prevState := c.state
	// An opt-out stops counting and cancels a send in flight now rather than
	// once the choice is saved, so a slow save leaves no window for a report.
	if !enabled {
		c.abortSendLocked()
		c.state = StateOff
	}
	c.mu.Unlock()

	now := c.now()
	choice := settings.UsageDataChoice{Enabled: enabled, DecidedAt: now}
	if err := c.save(func(s *settings.Settings) { s.UsageData = &choice }); err != nil {
		c.mu.Lock()
		c.state = prevState
		c.mu.Unlock()
		return c.Status(), fmt.Errorf("save usage-data choice: %w", err)
	}

	// Apply what was saved directly. Re-reading the store instead could fail
	// and leave the previous decision in force after an opt-out.
	state, source := resolve(c.env, hosted, &choice)
	c.mu.Lock()
	wasRecording := c.state.Recording()
	c.state, c.source = state, source
	switch {
	case state.Recording() && wasRecording:
		// Saying yes again keeps the period already being counted.
	case state.Recording():
		c.abortSendLocked()
		c.p = pending{PeriodStart: now}
		c.sampledAt = time.Time{}
		c.dirty = true
	default:
		c.discardLocked()
	}
	c.mu.Unlock()
	c.flush()
	return c.Status(), nil
}

func (c *Collector) run(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
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
	c.maybeSend(ctx, false)
}

func (c *Collector) sampleCluster() {
	// Checked before sampling: reading a cluster's shape walks the Pod cache.
	c.mu.Lock()
	due := c.state.Recording() && c.now().Sub(c.sampledAt) >= clusterSampleInterval
	c.mu.Unlock()
	if !due {
		return
	}
	key, shape, ok := c.opts.ClusterSample()
	if !ok {
		return
	}
	hashed := hashKey(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Recording() {
		return
	}
	c.sampledAt = c.now()
	if c.p.Clusters == nil {
		c.p.Clusters = map[string]ClusterShape{}
	}
	c.p.Clusters[hashed] = shape
	c.dirty = true
}

// maybeSend sends the day's report, or with early set whatever is pending
// however young the period and however recently a send failed. It takes the counts out before the request so
// recording continues into the next period, and puts them back if the
// request fails. A decision made while a request is in flight (the gen check)
// wins over the request's outcome.
func (c *Collector) maybeSend(ctx context.Context, early bool) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	now := c.now()
	// An early send ignores the retry wait: it is the last chance for these
	// counts, which a daily send cancelled by shutdown may just have returned.
	due := c.state.Recording() && !c.p.PeriodStart.IsZero() &&
		(early || (now.Sub(c.p.PeriodStart) >= reportInterval && now.After(c.nextAttempt)))
	if !due {
		c.mu.Unlock()
		return
	}
	taken := c.p
	c.p = pending{PeriodStart: now}
	c.dirty = true
	gen := c.gen
	sendCtx, cancel := context.WithCancel(ctx)
	c.cancelSend = cancel
	c.mu.Unlock()
	defer cancel()

	// A day with no recorded use is not a report: the UI was never opened.
	if !taken.empty() {
		report := c.buildReport(taken)
		switch {
		case version.IsDevelopmentBuild():
			logReport(report, "development build")
		default:
			if err := c.send(sendCtx, report); err != nil {
				c.mu.Lock()
				if c.gen == gen && errors.Is(err, errReportRefused) {
					log.Printf("[usage] Usage report dropped: %v", err)
					c.mu.Unlock()
					// Persist the drop now, or a restart would reload and resend it.
					c.flush()
					return
				}
				if c.gen == gen {
					log.Printf("[usage] Usage report not sent, retrying in a day: %v", err)
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
		c.cancelSend = nil
	}
	c.mu.Unlock()
	c.flush()
}

func logReport(r Report, why string) {
	body, _ := json.MarshalIndent(r, "", "  ")
	log.Printf("[usage] Usage report not sent (%s):\n%s", why, body)
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
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w: usage endpoint returned %d", errReportRefused, resp.StatusCode)
	default:
		return fmt.Errorf("usage endpoint returned %d", resp.StatusCode)
	}
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
		log.Printf("[usage] Ignoring unreadable %s: %v", c.path, err)
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
		log.Printf("[usage] Failed to save pending report: %v", err)
		return
	}
	c.dirty = false
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Shutdown saves the pending report, so counts from the last minute survive
// a restart, and with Options.SendOnShutdown sends it first.
func Shutdown() {
	c := current()
	if c == nil {
		return
	}
	if c.opts.SendOnShutdown {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownSendTimeout)
		c.maybeSend(ctx, true)
		cancel()
	}
	c.flush()
}
