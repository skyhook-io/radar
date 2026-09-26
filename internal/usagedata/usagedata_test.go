package usagedata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/internal/version"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestResolvePrecedence(t *testing.T) {
	yes := &settings.UsageDataChoice{Enabled: true}
	no := &settings.UsageDataChoice{Enabled: false}
	tests := []struct {
		name       string
		env        map[string]string
		hosted     bool
		choice     *settings.UsageDataChoice
		wantState  State
		wantSource Source
	}{
		{"nothing set is undecided", nil, false, nil, StateUndecided, SourceDefault},
		{"user yes", nil, false, yes, StateOn, SourceUser},
		{"user no", nil, false, no, StateOff, SourceUser},
		{"hosted install ignores a saved yes", nil, true, yes, StateOff, SourceDeployment},
		{"hosted beats env on", map[string]string{"RADAR_USAGE_REPORTING": "on"}, true, nil, StateOff, SourceDeployment},
		{"env on beats a saved no", map[string]string{"RADAR_USAGE_REPORTING": "on"}, false, no, StateOn, SourceEnv},
		{"env off beats user yes", map[string]string{"RADAR_USAGE_REPORTING": "false"}, false, yes, StateOff, SourceEnv},
		{"env log", map[string]string{"RADAR_USAGE_REPORTING": "log"}, false, nil, StateLog, SourceEnv},
		{"a typo in the env var means off", map[string]string{"RADAR_USAGE_REPORTING": "of"}, false, yes, StateOff, SourceEnv},
		{"DO_NOT_TRACK beats env on", map[string]string{"DO_NOT_TRACK": "1", "RADAR_USAGE_REPORTING": "on"}, false, yes, StateOff, SourceDoNotTrack},
		{"DO_NOT_TRACK=0 is not set", map[string]string{"DO_NOT_TRACK": "0"}, false, yes, StateOn, SourceUser},
		{"DO_NOT_TRACK=true", map[string]string{"DO_NOT_TRACK": "true"}, false, nil, StateOff, SourceDoNotTrack},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, source := resolve(envOf(tt.env), tt.hosted, tt.choice)
			if state != tt.wantState || source != tt.wantSource {
				t.Fatalf("got %s/%s, want %s/%s", state, source, tt.wantState, tt.wantSource)
			}
		})
	}
}

type harness struct {
	c     *Collector
	saved settings.Settings
	now   time.Time
	sent  []Report
	fail  error
}

func newHarness(t *testing.T, env map[string]string, hosted bool) *harness {
	t.Helper()
	h := &harness{now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	c := newCollector(Options{
		Hosted: func() bool { return hosted },
		Mode:   func() string { return "local" },
		ClusterSample: func() (string, ClusterShape, bool) {
			return "kube-system-uid-1", ClusterShape{KubernetesVersion: "1.33", Platform: "eks", Nodes: "10-19", Integrations: []string{"argo-cd", "karpenter"}}, true
		},
		Contexts: func() int { return 7 },
	})
	c.env = envOf(env)
	c.now = func() time.Time { return h.now }
	c.path = filepath.Join(t.TempDir(), "usage-report.json")
	c.p = pending{}
	c.load = func() (settings.Settings, error) { return h.saved, nil }
	c.save = func(fn func(*settings.Settings)) error { fn(&h.saved); return nil }
	c.send = func(_ context.Context, r Report) error {
		if h.fail != nil {
			return h.fail
		}
		h.sent = append(h.sent, r)
		return nil
	}
	h.c = c
	defaultMu.Lock()
	defaultC = c
	defaultMu.Unlock()
	t.Cleanup(func() {
		defaultMu.Lock()
		defaultC = nil
		defaultMu.Unlock()
	})
	c.refresh()
	return h
}

func releaseBuild(t *testing.T) {
	t.Helper()
	prev := version.Current
	version.SetCurrent("1.15.0")
	t.Cleanup(func() { version.SetCurrent(prev) })
}

func TestNothingRecordedBeforeConsent(t *testing.T) {
	h := newHarness(t, nil, false)
	RecordView("topology")
	RecordMCPTool("list_resources")
	st := h.c.Status()
	if st.State != StateUndecided || !st.CanChange {
		t.Fatalf("state = %s canChange = %v", st.State, st.CanChange)
	}
	if len(st.Preview.Views) != 0 || len(st.Preview.MCPTools) != 0 {
		t.Fatalf("recorded before consent: %+v", st.Preview)
	}
	if _, err := os.Stat(h.c.path); !os.IsNotExist(err) {
		t.Fatalf("pending file written before consent")
	}
}

func TestOptInRecordsOnlyAllowedNames(t *testing.T) {
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("topology")
	RecordView("topology")
	RecordView("/resources/secrets/prod/db-password")
	RecordMCPTool("list_resources")
	RecordMCPTool("Drop Table")
	st := h.c.Status()
	if st.Preview.Views["topology"] != 2 || len(st.Preview.Views) != 1 {
		t.Fatalf("views = %v", st.Preview.Views)
	}
	if st.Preview.MCPTools["list_resources"] != 1 || len(st.Preview.MCPTools) != 1 {
		t.Fatalf("tools = %v", st.Preview.MCPTools)
	}
	if st.NextReportAt == nil || !st.NextReportAt.Equal(h.now.Add(reportInterval)) {
		t.Fatalf("next report at %v", st.NextReportAt)
	}
	h.c.flush()
	if _, err := os.Stat(h.c.path); err != nil {
		t.Fatalf("pending report not saved: %v", err)
	}
}

func TestOptOutDeletesPending(t *testing.T) {
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	h.c.flush()
	if _, err := h.c.SetChoice(false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.c.path); !os.IsNotExist(err) {
		t.Fatalf("pending file survived opt-out")
	}
	RecordView("helm")
	if st := h.c.Status(); st.State != StateOff || len(st.Preview.Views) != 0 {
		t.Fatalf("after opt-out: %s %v", st.State, st.Preview.Views)
	}
}

func TestManagedChoiceCannotChange(t *testing.T) {
	for _, env := range []map[string]string{{"DO_NOT_TRACK": "1"}, {"RADAR_USAGE_REPORTING": "off"}} {
		h := newHarness(t, env, false)
		if _, err := h.c.SetChoice(true, ""); !errors.Is(err, ErrManaged) {
			t.Fatalf("env %v: err = %v, want ErrManaged", env, err)
		}
		if h.saved.UsageData != nil {
			t.Fatalf("env %v: choice saved despite managed state", env)
		}
	}
	h := newHarness(t, nil, true)
	if _, err := h.c.SetChoice(true, ""); !errors.Is(err, ErrManaged) {
		t.Fatalf("hosted: err = %v, want ErrManaged", err)
	}
}

func TestDailySend(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("timeline")
	h.now = h.now.Add(reportInterval - time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 0 {
		t.Fatalf("sent before a day passed")
	}
	h.now = h.now.Add(2 * time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 1 {
		t.Fatalf("sent %d reports, want 1", len(h.sent))
	}
	r := h.sent[0]
	if r.Views["timeline"] != 1 || r.Version != "1.15.0" || r.Mode != "local" {
		t.Fatalf("report = %+v", r)
	}
	if r.Clusters.Contexts != "5-9" || r.Clusters.Used != 1 || len(r.Clusters.Shapes[0].Integrations) != 2 {
		t.Fatalf("report clusters = %+v", r.Clusters)
	}
	if st := h.c.Status(); len(st.Preview.Views) != 0 || st.LastSentAt == nil {
		t.Fatalf("pending not reset after send: %+v", st)
	}
}

func TestEmptyDaySendsNothing(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 0 {
		t.Fatalf("sent a report for a day with no use")
	}
}

func TestFailedSendRetriesNextDay(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	h.fail = errors.New("offline")
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	h.fail = nil
	h.now = h.now.Add(time.Hour)
	h.c.tick(context.Background())
	if len(h.sent) != 0 {
		t.Fatalf("retried before a day passed")
	}
	h.now = h.now.Add(retryDelay)
	h.c.tick(context.Background())
	if len(h.sent) != 1 || h.sent[0].Views["helm"] != 1 {
		t.Fatalf("retry did not send the kept counts: %+v", h.sent)
	}
}

func TestDevelopmentBuildNeverSends(t *testing.T) {
	prev := version.Current
	version.SetCurrent("v1.14.1-55-gc092b8bc8")
	t.Cleanup(func() { version.SetCurrent(prev) })
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 0 {
		t.Fatalf("development build sent a report")
	}
}

func TestOptOutDuringSendDiscardsIt(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	var sawCancel bool
	h.c.send = func(ctx context.Context, _ Report) error {
		// The user opts out while the request is on the wire.
		if _, err := h.c.SetChoice(false, ""); err != nil {
			t.Fatal(err)
		}
		sawCancel = ctx.Err() != nil
		return ctx.Err()
	}
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if !sawCancel {
		t.Fatalf("in-flight send was not cancelled by opt-out")
	}
	st := h.c.Status()
	if st.State != StateOff || st.LastSentAt != nil || len(st.Preview.Views) != 0 {
		t.Fatalf("opt-out during send left state behind: %+v", st)
	}
	if _, err := os.Stat(h.c.path); !os.IsNotExist(err) {
		t.Fatalf("pending file recreated after opt-out")
	}
}

func TestCustomBuildVersionIsMasked(t *testing.T) {
	prev := version.Current
	version.SetCurrent("1.15.0-acme-internal")
	t.Cleanup(func() { version.SetCurrent(prev) })
	if version.BuildChannelName() != "custom" {
		t.Skipf("channel for test version is %q", version.BuildChannelName())
	}
	h := newHarness(t, nil, false)
	if got := h.c.Status().Preview.Version; got != "custom" {
		t.Fatalf("version = %q, want custom", got)
	}
}

func TestConcurrentRecordAndStatus(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			RecordView("topology")
			RecordMCPTool("list_resources")
		}
	}()
	for i := 0; i < 200; i++ {
		_ = h.c.Status()
	}
	<-done
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 1 || h.sent[0].Views["topology"] != 2000 {
		t.Fatalf("sent = %+v", h.sent)
	}
}

func TestFirstRunPromptOnlyForFreshInstallsOnce(t *testing.T) {
	h := newHarness(t, nil, false)
	installed := h.now.Add(-2 * time.Hour).Unix()
	h.c.opts.InstalledAt = func() int64 { return installed }
	if !h.c.Status().FirstRunPrompt {
		t.Fatalf("fresh install should get the prompt")
	}
	if err := MarkPromptShown(); err != nil {
		t.Fatal(err)
	}
	if h.c.Status().FirstRunPrompt || h.saved.UsagePromptShownAt == nil {
		t.Fatalf("prompt should not show twice")
	}
	// A restart re-reads settings and must still remember.
	h.c.promptShownAt = nil
	h.c.refresh()
	if h.c.Status().FirstRunPrompt {
		t.Fatalf("prompt came back after a restart")
	}
}

func TestFirstRunPromptSkipsUpgradesAndDecidedInstalls(t *testing.T) {
	h := newHarness(t, nil, false)
	h.c.opts.InstalledAt = func() int64 { return h.now.Add(-30 * 24 * time.Hour).Unix() }
	if h.c.Status().FirstRunPrompt {
		t.Fatalf("an install older than the window is an upgrade, asked in What's New instead")
	}
	h.c.opts.InstalledAt = func() int64 { return 0 }
	if h.c.Status().FirstRunPrompt {
		t.Fatalf("unknown install time must not prompt")
	}
	h.c.opts.InstalledAt = func() int64 { return h.now.Unix() }
	if _, err := h.c.SetChoice(false, ""); err != nil {
		t.Fatal(err)
	}
	if h.c.Status().FirstRunPrompt {
		t.Fatalf("an answered install must not prompt")
	}
	m := newHarness(t, map[string]string{"DO_NOT_TRACK": "1"}, false)
	m.c.opts.InstalledAt = func() int64 { return m.now.Unix() }
	if m.c.Status().FirstRunPrompt {
		t.Fatalf("a managed install must not prompt")
	}
}

func TestSharedChoiceRecordsWhoDecided(t *testing.T) {
	h := newHarness(t, nil, false)
	h.c.opts.Shared = func() bool { return true }
	st := h.c.Status()
	if st.State != StateUndecided || !st.CanChange || !st.Shared {
		t.Fatalf("shared install should be askable and marked shared: %+v", st)
	}
	st, err := h.c.SetChoice(true, "dana@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateOn || st.DecidedBy != "dana@example.com" || st.DecidedAt == nil {
		t.Fatalf("status after team opt-in = %+v", st)
	}
	if h.saved.UsageData == nil || h.saved.UsageData.DecidedBy != "dana@example.com" {
		t.Fatalf("decider not saved: %+v", h.saved.UsageData)
	}
	if st.NextReportAt == nil {
		t.Fatalf("shared opt-in should start reporting")
	}
}

func TestDeciderNeverReported(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	h.c.opts.Shared = func() bool { return true }
	if _, err := h.c.SetChoice(true, "dana@example.com"); err != nil {
		t.Fatal(err)
	}
	RecordView("home")
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	body, _ := json.Marshal(h.sent)
	if strings.Contains(string(body), "dana") {
		t.Fatalf("report leaked the decider: %s", body)
	}
}

func TestStaleRefreshCannotUndoOptOut(t *testing.T) {
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	// A background refresh reads the store while the user is still opted in...
	staleLoad := h.c.load
	var once bool
	h.c.load = func() (settings.Settings, error) {
		s, err := staleLoad()
		if !once {
			once = true
			// ...and the user opts out before that refresh commits.
			h.c.load = staleLoad
			if _, err := h.c.SetChoice(false, ""); err != nil {
				t.Fatal(err)
			}
		}
		return s, err
	}
	h.c.refresh()
	RecordView("helm")
	st := h.c.Status()
	if st.State != StateOff || len(st.Preview.Views) != 0 {
		t.Fatalf("stale refresh undid the opt-out: %s %v", st.State, st.Preview.Views)
	}
}

func TestLoadErrorKeepsLastKnownState(t *testing.T) {
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	good := h.c.load
	h.c.load = func() (settings.Settings, error) { return settings.Settings{}, errors.New("apiserver blip") }
	h.c.refresh()
	st := h.c.Status()
	if st.State != StateOn || st.Preview.Views["helm"] != 1 || st.FirstRunPrompt {
		t.Fatalf("a failed read changed state or dropped counts: %+v", st)
	}
	h.c.load = good
}

func TestWhatsNewAsksAgainOnlyAfterAWhileAndNeverAfterNo(t *testing.T) {
	h := newHarness(t, nil, false)
	if st := h.c.Status(); !st.Ask {
		t.Fatalf("never-asked user should be asked: %+v", st)
	}
	if err := MarkPromptShown(); err != nil {
		t.Fatal(err)
	}
	if st := h.c.Status(); st.Ask {
		t.Fatalf("asked again right after closing the question")
	}
	h.now = h.now.Add(reaskInterval - time.Hour)
	if st := h.c.Status(); st.Ask {
		t.Fatalf("asked again before the interval")
	}
	h.now = h.now.Add(2 * time.Hour)
	if st := h.c.Status(); !st.Ask {
		t.Fatalf("unanswered question should come back after the interval")
	}
	if _, err := h.c.SetChoice(false, ""); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(10 * reaskInterval)
	if st := h.c.Status(); st.Ask || st.FirstRunPrompt {
		t.Fatalf("an explicit no must never be asked again: %+v", st)
	}
}

func TestShutdownSendsAYoungPeriodOnlyWhenAsked(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	h.now = h.now.Add(time.Hour)
	Shutdown()
	if len(h.sent) != 0 {
		t.Fatalf("local shutdown sent early: %+v", h.sent)
	}
	if _, err := os.Stat(h.c.path); err != nil {
		t.Fatalf("shutdown did not save the pending report: %v", err)
	}

	h.c.opts.SendOnShutdown = true
	Shutdown()
	if len(h.sent) != 1 || h.sent[0].Views["helm"] != 1 {
		t.Fatalf("in-cluster shutdown should send what is pending: %+v", h.sent)
	}
	Shutdown()
	if len(h.sent) != 1 {
		t.Fatalf("an empty period must not be sent: %d reports", len(h.sent))
	}
}

func TestOptOutRemovesStrayReportFile(t *testing.T) {
	h := newHarness(t, nil, false)
	if err := os.WriteFile(h.c.path, []byte(`{"views":{"helm":3}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetChoice(false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.c.path); !os.IsNotExist(err) {
		t.Fatalf("report file survived an opt-out")
	}
}

func TestOptOutTakesEffectEvenIfTheStoreCannotBeReread(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	h.c.load = func() (settings.Settings, error) { return settings.Settings{}, errors.New("apiserver blip") }
	st, err := h.c.SetChoice(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateOff || len(st.Preview.Views) != 0 {
		t.Fatalf("opt-out did not take effect: %s %v", st.State, st.Preview.Views)
	}
	RecordView("helm")
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	Shutdown()
	if len(h.sent) != 0 {
		t.Fatalf("sent after opt-out: %+v", h.sent)
	}
}

func TestOptOutStopsCountingBeforeTheSaveFinishes(t *testing.T) {
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	save := h.c.save
	h.c.save = func(fn func(*settings.Settings)) error {
		RecordView("helm") // lands while the opt-out is being saved
		if Recording() {
			t.Errorf("still recording while the opt-out is saved")
		}
		return save(fn)
	}
	if _, err := h.c.SetChoice(false, ""); err != nil {
		t.Fatal(err)
	}
	if h.c.Status().Preview.Views["helm"] != 0 {
		t.Fatalf("counted during the opt-out")
	}
}

func TestFailedOptOutSaveKeepsRecording(t *testing.T) {
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	h.c.save = func(func(*settings.Settings)) error { return errors.New("read-only store") }
	if _, err := h.c.SetChoice(false, ""); err == nil {
		t.Fatal("want the save error")
	}
	if st := h.c.Status(); st.State != StateOn || st.Preview.Views["helm"] != 1 {
		t.Fatalf("an unsaved opt-out changed state or dropped counts: %s %v", st.State, st.Preview.Views)
	}
}

func TestShutdownSendIgnoresTheRetryWait(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	h.c.opts.SendOnShutdown = true
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	h.c.mu.Lock()
	h.c.nextAttempt = h.now.Add(retryDelay) // a daily send just failed or was cancelled
	h.c.mu.Unlock()
	Shutdown()
	if len(h.sent) != 1 {
		t.Fatalf("shutdown send held back by the retry wait: %d reports", len(h.sent))
	}
}

func TestActiveTimeAloneIsUse(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordActive(5)
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 1 {
		t.Fatalf("a day spent on one screen sent nothing")
	}
}

// Status must not hold the collector's lock while asking the cluster about
// itself: every recorded API request takes that lock.
func TestStatusDoesNotHoldTheLockForClusterLookups(t *testing.T) {
	h := newHarness(t, nil, false)
	h.c.opts.InstalledAt = func() int64 { Recording(); return 0 }
	h.c.opts.Shared = func() bool { Recording(); return false }
	done := make(chan struct{})
	go func() { h.c.Status(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Status deadlocked calling back into the collector")
	}
}

func TestPreviewReusesARecentClusterSample(t *testing.T) {
	h := newHarness(t, nil, false)
	calls := 0
	h.c.opts.ClusterSample = func() (string, ClusterShape, bool) {
		calls++
		return "k", ClusterShape{Platform: "eks"}, true
	}
	h.c.Status()
	h.c.Status()
	if calls != 1 {
		t.Fatalf("preview sampled the cluster %d times", calls)
	}
	h.now = h.now.Add(clusterSampleInterval + time.Second)
	if st := h.c.Status(); calls != 2 || st.Preview.Clusters.Shapes[0].Platform != "eks" {
		t.Fatalf("stale preview not refreshed: %d calls", calls)
	}
}

func TestSayingYesAgainKeepsThePeriod(t *testing.T) {
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	if h.c.Status().Preview.Views["helm"] != 1 {
		t.Fatalf("a repeated yes dropped the counts")
	}
}

func TestLaterChoiceWinsWhenSavesOverlap(t *testing.T) {
	h := newHarness(t, nil, false)
	release := make(chan struct{})
	save := h.c.save
	first := true
	h.c.save = func(fn func(*settings.Settings)) error {
		if first {
			first = false
			<-release // the opt-in's save is slow
		}
		return save(fn)
	}
	done := make(chan struct{})
	go func() { _, _ = h.c.SetChoice(true, ""); close(done) }()
	time.Sleep(20 * time.Millisecond)
	optedOut := make(chan struct{})
	go func() { _, _ = h.c.SetChoice(false, ""); close(optedOut) }()
	close(release)
	<-done
	<-optedOut
	if h.saved.UsageData == nil || h.saved.UsageData.Enabled {
		t.Fatalf("stored choice = %+v, want the later opt-out", h.saved.UsageData)
	}
}

func TestRefusedReportIsDroppedNotRetried(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	h.fail = fmt.Errorf("%w: usage endpoint returned 400", errReportRefused)
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if st := h.c.Status(); len(st.Preview.Views) != 0 {
		t.Fatalf("a refused report was kept for retry: %v", st.Preview.Views)
	}

	RecordView("topology")
	h.fail = errors.New("connection refused")
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if st := h.c.Status(); st.Preview.Views["topology"] != 1 {
		t.Fatalf("a network failure should keep the counts for retry: %v", st.Preview.Views)
	}
}
