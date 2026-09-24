package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/poll"
)

// pollTestEnv points every seam at test doubles: an empty home, a clock inside
// the round, an old install, and a fake releases.skyhook.io.
func pollTestEnv(t *testing.T, upstream http.HandlerFunc) (*Server, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RADAR_POLL", "")

	prevStore, prevURL, prevNow, prevInstalled, prevMode, prevDev := pollStore, pollSubmitURL, pollNow, pollInstalled, pollMode, pollDevBuild
	t.Cleanup(func() {
		pollStore, pollSubmitURL, pollNow, pollInstalled, pollMode, pollDevBuild = prevStore, prevURL, prevNow, prevInstalled, prevMode, prevDev
	})
	pollMode = func() k8s.DeploymentMode { return k8s.DeploymentModeLocal }
	pollDevBuild = func() bool { return false }

	now := poll.Current.StartsAt.Add(24 * time.Hour)
	pollStore = poll.NewStore(filepath.Join(home, ".radar", "poll.json"))
	pollNow = func() time.Time { return now }
	pollInstalled = func(context.Context, string) time.Time { return now.Add(-60 * 24 * time.Hour) }

	var up *httptest.Server
	if upstream != nil {
		up = httptest.NewServer(upstream)
		t.Cleanup(up.Close)
		pollSubmitURL = up.URL
	}
	return &Server{}, up
}

func pollPost(t *testing.T, s *Server, handler func(http.ResponseWriter, *http.Request), body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://localhost:9280/api/poll/submit", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:9280")
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

const validSubmission = `{"submissionId":"0123456789abcdef-0001","answers":{"q2":{"choices":["one"]},"q7":{"text":"faster topology"}}}`

func TestPollSubmitForwardsNormalizedAnonymousAnswers(t *testing.T) {
	var got pollUpstreamRequest
	s, _ := pollTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode upstream: %v", err)
		}
		w.Write([]byte(`{"answers":"ok","contact":"none"}`))
	})

	rec := pollPost(t, s, s.handlePollSubmit, validSubmission, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got.Round != poll.Current.ID || got.SubmissionID != "0123456789abcdef-0001" {
		t.Fatalf("upstream got %+v", got)
	}
	if got.Facts.Age != "1_6_months" || got.Facts.Mode != "local" || got.Facts.OS == "" {
		t.Fatalf("facts = %+v", got.Facts)
	}
	if got.Email != "" || got.WantsCall {
		t.Fatalf("contact fields sent without an email: %+v", got)
	}
	raw, _ := json.Marshal(got)
	for _, identifying := range []string{"install", "browser", "\"ip\"", "\"t\""} {
		if strings.Contains(string(raw), identifying) {
			t.Fatalf("upstream payload carries %s: %s", identifying, raw)
		}
	}
	if st := pollStore.Load(); st.SubmittedRound != poll.Current.ID {
		t.Fatalf("submission not recorded locally: %+v", st)
	}
}

func TestPollSubmitRefusals(t *testing.T) {
	var calls atomic.Int32
	s, _ := pollTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"answers":"ok","contact":"none"}`))
	})

	cases := []struct {
		name   string
		body   string
		mutate func(*http.Request)
		want   int
	}{
		{"cross-site fetch", validSubmission, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		{"hostile origin", validSubmission, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		{"text/plain body", validSubmission, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		{"over the size cap", `{"submissionId":"0123456789abcdef-0001","answers":{"q7":{"text":"` + strings.Repeat("a", 17<<10) + `"}}}`, nil, http.StatusRequestEntityTooLarge},
		{"bad submission id", `{"submissionId":"x","answers":{}}`, nil, http.StatusBadRequest},
		{"unknown question", `{"submissionId":"0123456789abcdef-0001","answers":{"q99":{"choices":["a"]}}}`, nil, http.StatusBadRequest},
		{"call without email", `{"submissionId":"0123456789abcdef-0001","answers":{},"wantsCall":true}`, nil, http.StatusBadRequest},
		{"malformed email", `{"submissionId":"0123456789abcdef-0001","answers":{},"email":"Roy <roy@example.com>"}`, nil, http.StatusBadRequest},
		{"unknown field", `{"submissionId":"0123456789abcdef-0001","answers":{},"installId":"x"}`, nil, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := pollPost(t, s, s.handlePollSubmit, tc.body, tc.mutate)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("refused submissions reached upstream %d times", calls.Load())
	}
}

func TestPollSubmitUpstreamOutcomes(t *testing.T) {
	cases := []struct {
		name     string
		upstream http.HandlerFunc
		want     int
	}{
		{"round closed upstream", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusGone) }, http.StatusGone},
		{"upstream error", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }, http.StatusBadGateway},
		{"upstream garbage", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`<html>`)) }, http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := pollTestEnv(t, tc.upstream)
			rec := pollPost(t, s, s.handlePollSubmit, validSubmission, nil)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if st := pollStore.Load(); st.SubmittedRound != "" {
				t.Fatal("failed submission recorded as submitted")
			}
		})
	}
}

func TestPollSubmitContactOnlySkipsAnswers(t *testing.T) {
	var got pollUpstreamRequest
	s, _ := pollTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.Write([]byte(`{"answers":"skipped","contact":"ok"}`))
	})
	body := `{"submissionId":"0123456789abcdef-0001","answers":{"q99":{"choices":["ignored"]}},"email":"sam@example.com","wantsCall":true,"contactOnly":true}`
	rec := pollPost(t, s, s.handlePollSubmit, body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !got.ContactOnly || got.Answers != nil || got.Email != "sam@example.com" || !got.WantsCall {
		t.Fatalf("upstream got %+v", got)
	}
}

func TestPollStatusHidesFromTunnelRuns(t *testing.T) {
	s, _ := pollTestEnv(t, nil)
	s.cloudConnectCfg.CloudTunnelConfigured = true
	rec := httptest.NewRecorder()
	s.handlePollStatus(rec, httptest.NewRequest(http.MethodGet, "/api/poll", nil))
	var resp pollStatusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Eligible || resp.Reason != "cloud" {
		t.Fatalf("status = %+v, want ineligible for a --cloud-url run", resp)
	}
	if rec := pollPost(t, s, s.handlePollShown, `{}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("shown on a tunnel run = %d, want 404", rec.Code)
	}
}

func TestPollInClusterKeepsNoServerState(t *testing.T) {
	s, _ := pollTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var got pollUpstreamRequest
		json.NewDecoder(r.Body).Decode(&got)
		if got.Facts.Method != "helm" || got.Facts.Mode != "in-cluster" {
			t.Errorf("in-cluster facts = %+v", got.Facts)
		}
		w.Write([]byte(`{"answers":"ok","contact":"none"}`))
	})
	pollMode = func() k8s.DeploymentMode { return k8s.DeploymentModeInCluster }
	pollPost(t, s, s.handlePollDismiss, `{"kind":"never"}`, nil)
	pollPost(t, s, s.handlePollShown, `{}`, nil)
	if rec := pollPost(t, s, s.handlePollSubmit, validSubmission, nil); rec.Code != http.StatusOK {
		t.Fatalf("submit = %d: %s", rec.Code, rec.Body)
	}
	if st := pollStore.Load(); st != (poll.State{}) {
		t.Fatalf("in-cluster wrote shared state: %+v", st)
	}
}

func TestPollStatusRespectsOptOut(t *testing.T) {
	s, _ := pollTestEnv(t, nil)
	t.Setenv("RADAR_POLL", "off")
	rec := httptest.NewRecorder()
	s.handlePollStatus(rec, httptest.NewRequest(http.MethodGet, "/api/poll", nil))
	var resp pollStatusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Eligible || resp.Reason != "disabled" {
		t.Fatalf("status = %+v", resp)
	}
	// A tab opened before the switch was flipped must not be able to send.
	if rec := pollPost(t, s, s.handlePollSubmit, validSubmission, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("submit with RADAR_POLL=off = %d, want 404", rec.Code)
	}
}

func TestPollDismissAndShownInterleave(t *testing.T) {
	s, _ := pollTestEnv(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); pollPost(t, s, s.handlePollShown, `{}`, nil) }()
		go func() { defer wg.Done(); pollPost(t, s, s.handlePollDismiss, `{"kind":"never"}`, nil) }()
	}
	wg.Wait()
	st := pollStore.Load()
	if st.NeverAt.IsZero() || st.ShownAt.IsZero() {
		t.Fatalf("state after interleaving = %+v", st)
	}
	if rec := pollPost(t, s, s.handlePollDismiss, `{"kind":"later"}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d", rec.Code)
	}
}
