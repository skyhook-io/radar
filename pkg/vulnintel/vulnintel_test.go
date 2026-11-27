package vulnintel

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return buf.Bytes()
}

func TestParseKEV(t *testing.T) {
	c, err := ParseKEV(bytes.NewReader(fixture(t, "kev.json")))
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "2026.11.20" || c.Len() != 3 {
		t.Fatalf("version %q len %d", c.Version, c.Len())
	}
	log4j, ok := c.Lookup("cve-2021-44228")
	if !ok || log4j.RansomwareUse != "known" || log4j.DateAdded != "2021-12-10" || log4j.DueDate != "2021-12-24" {
		t.Fatalf("log4shell: %+v %v", log4j, ok)
	}
	if k, _ := c.Lookup("CVE-2023-44487"); k.RansomwareUse != "unknown" {
		t.Fatalf("rapid reset ransomware use %q, want unknown", k.RansomwareUse)
	}
	if _, ok := c.Lookup("CVE-2099-0001"); ok {
		t.Fatal("found a CVE the catalog doesn't list")
	}
	var nilCatalog *KEVCatalog
	if _, ok := nilCatalog.Lookup("CVE-2021-44228"); ok {
		t.Fatal("a nil catalog found an entry")
	}
}

func TestParseKEVRejectsEmpty(t *testing.T) {
	if _, err := ParseKEV(strings.NewReader(`{"vulnerabilities":[]}`)); err == nil {
		t.Fatal("an empty catalog parsed")
	}
	if _, err := ParseKEV(strings.NewReader(`<html>`)); err == nil {
		t.Fatal("HTML parsed as a catalog")
	}
}

func TestParseEPSSPlainAndGzip(t *testing.T) {
	plain := fixture(t, "epss.csv")
	for name, body := range map[string][]byte{"plain": plain, "gzip": gz(t, plain)} {
		t.Run(name, func(t *testing.T) {
			s, err := ParseEPSS(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if s.Len() != 5 || s.ModelVersion != "v2026.06.15" || s.ScoreDate.Format("2006-01-02") != "2026-11-22" {
				t.Fatalf("len %d model %q date %v", s.Len(), s.ModelVersion, s.ScoreDate)
			}
			e, ok := s.Lookup("CVE-2020-15999")
			if !ok || e.Score != 0.63894 || e.Percentile != 0.99242 {
				t.Fatalf("CVE-2020-15999: %+v %v", e, ok)
			}
			if _, ok := s.Lookup("GHSA-xxxx-yyyy-zzzz"); ok {
				t.Fatal("a GHSA ID has a score")
			}
			if _, ok := s.Lookup("CVE-2023-0001"); ok {
				t.Fatal("an unscored CVE has a score")
			}
		})
	}
}

func TestParseEPSSRejectsWrongColumns(t *testing.T) {
	if _, err := ParseEPSS(strings.NewReader("a,b\n1,2\n")); err == nil {
		t.Fatal("a file without the EPSS columns parsed")
	}
}

func TestStoreLoadsFromURLAndFile(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/kev.json":
			_, _ = w.Write(fixture(t, "kev.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	epssPath := filepath.Join(dir, "epss.csv.gz")
	if err := os.WriteFile(epssPath, gz(t, fixture(t, "epss.csv")), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(Config{KEVSource: srv.URL + "/kev.json", EPSSSource: epssPath, CacheDir: filepath.Join(dir, "cache")})
	defer s.Close()
	if hits.Load() != 0 {
		t.Fatal("the store fetched before anything asked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.WaitLoaded(ctx)
	snap := s.Snapshot()
	if snap.KEVStatus.State != StateReady || snap.KEVStatus.Origin != OriginMirror || snap.KEVStatus.Count != 3 {
		t.Fatalf("KEV status %+v", snap.KEVStatus)
	}
	if snap.EPSSStatus.State != StateReady || snap.EPSSStatus.Origin != OriginFile || snap.EPSSStatus.Version != "2026-11-22" {
		t.Fatalf("EPSS status %+v", snap.EPSSStatus)
	}
	if _, ok := snap.KEV.Lookup("CVE-2023-44487"); !ok {
		t.Fatal("KEV not loaded")
	}
	if _, err := os.Stat(filepath.Join(dir, "cache", "kev.json")); err != nil {
		t.Fatalf("KEV not cached: %v", err)
	}
}

func TestStoreUnavailableNeverReadsAsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	s := NewStore(Config{KEVSource: srv.URL + "/kev.json", EPSSSource: "off"})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.WaitLoaded(ctx)
	snap := s.Snapshot()
	if snap.KEV != nil || snap.KEVStatus.State != StateUnavailable || !strings.Contains(snap.KEVStatus.Error, "403") {
		t.Fatalf("KEV %+v", snap.KEVStatus)
	}
	if strings.Contains(snap.KEVStatus.Error, srv.URL) {
		t.Fatalf("the error names the feed URL: %q", snap.KEVStatus.Error)
	}
	if snap.EPSSStatus.State != StateDisabled {
		t.Fatalf("EPSS %+v", snap.EPSSStatus)
	}
}

func TestStoreKeepsCachedCopyWhenOffline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kev.json"), fixture(t, "kev.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "kev.json"), old, old)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	s := NewStore(Config{KEVSource: srv.URL + "/kev.json", EPSSSource: "off", CacheDir: dir})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.WaitLoaded(ctx)
	st := s.Snapshot().KEVStatus
	if st.State != StateReady || !st.Stale || st.Error == "" {
		t.Fatalf("status %+v", st)
	}
}

// A manual refresh reloads at once, and a second one within five minutes is
// refused with how long to wait, whoever asks.
func TestRefreshNowIsRateLimited(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(fixture(t, "kev.json"))
	}))
	defer srv.Close()
	s := NewStore(Config{KEVSource: srv.URL + "/kev.json", EPSSSource: "off"})
	defer s.Close()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.WaitLoaded(ctx)
	if got := hits.Load(); got != 1 {
		t.Fatalf("first load fetched %d times", got)
	}
	if _, err := s.RefreshNow(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() != 2 {
		t.Fatalf("refresh didn't reload: %d fetches", hits.Load())
	}
	if st := s.Snapshot().KEVStatus; st.CheckedAt == nil || st.State != StateReady {
		t.Fatalf("status after refresh: %+v", st)
	}
	now = now.Add(2 * time.Minute)
	if wait, err := s.RefreshNow(); !errors.Is(err, ErrRefreshTooSoon) || wait != 3*time.Minute {
		t.Fatalf("second refresh within 5 minutes: %v, wait %v", err, wait)
	}
	now = now.Add(3 * time.Minute)
	if _, err := s.RefreshNow(); err != nil {
		t.Fatalf("refresh after 5 minutes: %v", err)
	}
	off := NewStore(Config{KEVSource: "off", EPSSSource: "off"})
	defer off.Close()
	if _, err := off.RefreshNow(); !errors.Is(err, ErrFeedsOff) {
		t.Fatalf("feeds off: %v", err)
	}
}

// A failed load records when it was tried, next to the last good copy.
func TestFailedLoadRecordsCheckedAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	s := NewStore(Config{KEVSource: srv.URL + "/kev.json", EPSSSource: "off"})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.WaitLoaded(ctx)
	if st := s.Snapshot().KEVStatus; st.CheckedAt == nil || st.LoadedAt != nil {
		t.Fatalf("status: %+v", st)
	}
}

// With downloads off the store makes no request: a URL feed reads as off,
// and a file feed still loads.
func TestNoDownloadsMakesNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	dir := t.TempDir()
	epssPath := filepath.Join(dir, "epss.csv")
	if err := os.WriteFile(epssPath, fixture(t, "epss.csv"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(Config{KEVSource: srv.URL + "/kev.json", EPSSSource: epssPath, NoDownloads: true, CacheDir: dir})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.WaitLoaded(ctx)
	if _, err := s.RefreshNow(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	snap := s.Snapshot()
	if hits.Load() != 0 {
		t.Fatalf("%d requests with downloads off", hits.Load())
	}
	if snap.KEVStatus.State != StateDisabled || !snap.KEVStatus.DownloadsOff {
		t.Fatalf("KEV %+v", snap.KEVStatus)
	}
	if snap.EPSSStatus.State != StateReady || snap.EPSSStatus.DownloadsOff {
		t.Fatalf("EPSS from a file %+v", snap.EPSSStatus)
	}
}

// A cached copy younger than the refresh interval is used as it is: a
// restart doesn't download the feed again.
func TestFreshCacheIsNotDownloadedAgain(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(fixture(t, "kev.json"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kev.json"), fixture(t, "kev.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "kev.json"), recent, recent)
	s := NewStore(Config{KEVSource: srv.URL + "/kev.json", EPSSSource: "off", CacheDir: dir})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.WaitLoaded(ctx)
	st := s.Snapshot().KEVStatus
	if hits.Load() != 0 || st.State != StateReady || st.Stale || st.LoadedAt == nil || !st.LoadedAt.Equal(recent) {
		t.Fatalf("%d downloads, status %+v", hits.Load(), st)
	}
}

// A feed that fails is retried on its own: the other one isn't downloaded
// again until its own refresh is due.
func TestFailingFeedRetriesAlone(t *testing.T) {
	var kevHits, epssHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/kev.json" {
			kevHits.Add(1)
			_, _ = w.Write(fixture(t, "kev.json"))
			return
		}
		epssHits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	s := NewStore(Config{KEVSource: srv.URL + "/kev.json", EPSSSource: srv.URL + "/epss.csv.gz", RetryAfter: 20 * time.Millisecond})
	defer s.Close()
	s.Snapshot()
	deadline := time.Now().Add(5 * time.Second)
	for epssHits.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if epssHits.Load() < 3 || kevHits.Load() != 1 {
		t.Fatalf("KEV fetched %d times, EPSS %d", kevHits.Load(), epssHits.Load())
	}
	snap := s.Snapshot()
	if snap.KEVStatus.State != StateReady || snap.EPSSStatus.State != StateUnavailable {
		t.Fatalf("KEV %+v, EPSS %+v", snap.KEVStatus, snap.EPSSStatus)
	}
}

// A catalog whose entries all lack a CVE ID is refused, never a ready
// catalog that lists nothing.
func TestParseKEVRejectsEntriesWithoutIDs(t *testing.T) {
	if _, err := ParseKEV(strings.NewReader(`{"vulnerabilities":[{"cveID":" "},{"cveID":""}]}`)); err == nil {
		t.Fatal("a catalog without CVE IDs parsed")
	}
}

// A download may be redirected, but never from https to plain http.
func TestRedirectFromHTTPSToHTTPIsRefused(t *testing.T) {
	req := func(u string) *http.Request {
		r, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	from := []*http.Request{req("https://mirror.example/kev.json")}
	if err := noDowngrade(req("http://mirror.example/kev.json"), from); err == nil {
		t.Fatal("https to http was followed")
	}
	if err := noDowngrade(req("https://cdn.example/kev.json"), from); err != nil {
		t.Fatalf("https to https: %v", err)
	}
}

// Closing a store nothing has read doesn't leave a later wait hanging.
func TestWaitLoadedReturnsAfterClose(t *testing.T) {
	s := NewStore(Config{KEVSource: "off", EPSSSource: "off"})
	s.Close()
	done := make(chan struct{})
	go func() { s.WaitLoaded(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitLoaded hung after Close")
	}
}
