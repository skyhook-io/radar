package vulnintel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Feed states. Only StateReady means a CVE missing from the feed is
// missing from it; every other state leaves that unknown.
const (
	StateReady       = "ready"
	StateLoading     = "loading"
	StateUnavailable = "unavailable"
	StateDisabled    = "disabled"
)

// Origins say where a feed is read from without naming the URL or path,
// which are the operator's and can carry credentials.
const (
	OriginPublic = "public"
	OriginMirror = "mirror"
	OriginFile   = "file"
)

// FeedStatus describes one feed's copy.
type FeedStatus struct {
	State  string `json:"state"`
	Origin string `json:"origin,omitempty"`
	// DownloadsOff is set on a disabled feed whose source is a URL while
	// feed downloads are turned off (Config.NoDownloads): a file would load.
	DownloadsOff bool `json:"downloadsOff,omitempty"`
	// Version is KEV's catalog version or EPSS's score date.
	Version string `json:"version,omitempty"`
	Count   int    `json:"count,omitempty"`
	// LoadedAt is when the copy in use was read; CheckedAt when Radar last
	// tried to load the feed, whether or not that worked.
	LoadedAt  *time.Time `json:"loadedAt,omitempty"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// Stale is true when the copy in use is older than StaleAfter because
	// newer loads failed.
	Stale bool `json:"stale,omitempty"`
	// Error is the last load's failure, also when an older copy is in use.
	Error string `json:"error,omitempty"`
}

// StaleAfter is when a copy that couldn't be refreshed reads as stale.
const StaleAfter = 72 * time.Hour

// Snapshot is the feeds as one read sees them. A nil catalog means its
// status isn't ready.
type Snapshot struct {
	KEV        *KEVCatalog
	EPSS       *EPSSScores
	KEVStatus  FeedStatus
	EPSSStatus FeedStatus
}

// Config says where each feed comes from. A source is an http(s) URL or a
// file path, re-read on each refresh so a mounted file can be updated in
// place; "off" or empty turns the feed off.
type Config struct {
	KEVSource  string
	EPSSSource string
	// CacheDir, when set, keeps the last good copy of each feed so a restart
	// without network access still has one.
	CacheDir string
	// Refresh is how often feeds are reloaded; RetryAfter how soon after a
	// failure.
	Refresh    time.Duration
	RetryAfter time.Duration
	Client     *http.Client
	// NoDownloads keeps the store from making any network request: an
	// http(s) source is treated as off (FeedStatus.DownloadsOff), and a
	// file source is still read.
	NoDownloads bool
}

// Store loads the feeds in the background, starting on the first Snapshot
// so a Radar nobody opens the Vulnerabilities page on never fetches them.
// Each feed keeps its own schedule: reloaded Refresh after it last loaded
// (a cached copy counts, so a restart doesn't download it again), and
// RetryAfter after a failure, without reloading the other feed.
type Store struct {
	cfg   Config
	start sync.Once
	stop  context.CancelFunc
	done  chan struct{}

	mu        sync.RWMutex
	kev, epss feed
	// loaded fires after the first load attempt of both feeds.
	loaded chan struct{}

	// kick asks the loader to reload now (RefreshNow), carrying when it was
	// asked; lastManual is when someone last did, for the rate limit.
	kick       chan time.Time
	manualMu   sync.Mutex
	lastManual time.Time
	clock      func() time.Time
}

// feed is one feed's source, copy and schedule. status and data are guarded
// by Store.mu; due and checked (when the last attempt ended) are the
// loader's alone.
type feed struct {
	source    string
	cacheName string
	parse     func([]byte) (any, FeedStatus, error)
	status    FeedStatus
	// data is the parsed copy in use: *KEVCatalog or *EPSSScores.
	data    any
	due     time.Time
	checked time.Time
}

func (f *feed) on() bool { return !off(f.source) }

// ManualRefreshInterval is how often anyone may ask for a reload: the feeds
// change daily, and each reload downloads several MB from a public server.
const ManualRefreshInterval = 5 * time.Minute

// ErrRefreshTooSoon: a reload was asked for within ManualRefreshInterval of
// the last one.
var ErrRefreshTooSoon = errors.New("the feeds were refreshed less than 5 minutes ago")

// ErrFeedsOff: both feeds are turned off, so there is nothing to reload.
var ErrFeedsOff = errors.New("both feeds are turned off on this Radar")

// RefreshNow asks the loader to reload both feeds now, at most once per
// ManualRefreshInterval across all callers. It returns at once; the
// feeds' status says when the reload finished. wait is how long until the
// next reload may be asked for when it returns ErrRefreshTooSoon.
func (s *Store) RefreshNow() (wait time.Duration, err error) {
	if !s.kev.on() && !s.epss.on() {
		return 0, ErrFeedsOff
	}
	s.ensureStarted()
	s.manualMu.Lock()
	defer s.manualMu.Unlock()
	now := s.clock()
	if !s.lastManual.IsZero() {
		if left := ManualRefreshInterval - now.Sub(s.lastManual); left > 0 {
			return left, ErrRefreshTooSoon
		}
	}
	s.lastManual = now
	select {
	case s.kick <- time.Now():
	default:
		// A reload is already queued.
	}
	return 0, nil
}

// NewStore returns a store that hasn't loaded anything yet.
func NewStore(cfg Config) *Store {
	if cfg.Refresh <= 0 {
		cfg.Refresh = 24 * time.Hour
	}
	if cfg.RetryAfter <= 0 {
		cfg.RetryAfter = time.Hour
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	s := &Store{cfg: cfg, loaded: make(chan struct{}), done: make(chan struct{}), kick: make(chan time.Time, 1), clock: time.Now}
	s.kev = newFeed(cfg.KEVSource, DefaultKEVURL, cfg.NoDownloads, "kev.json", func(b []byte) (any, FeedStatus, error) {
		c, err := ParseKEV(bytes.NewReader(b))
		if err != nil {
			return nil, FeedStatus{}, err
		}
		return c, FeedStatus{Version: c.Version, Count: c.Len()}, nil
	})
	s.epss = newFeed(cfg.EPSSSource, DefaultEPSSURL, cfg.NoDownloads, "epss.csv.gz", func(b []byte) (any, FeedStatus, error) {
		e, err := ParseEPSS(bytes.NewReader(b))
		if err != nil {
			return nil, FeedStatus{}, err
		}
		return e, FeedStatus{Version: epssVersion(e), Count: e.Len()}, nil
	})
	return s
}

// newFeed resolves a source: with downloads off, a URL is off.
func newFeed(source, public string, noDownloads bool, cacheName string, parse func([]byte) (any, FeedStatus, error)) feed {
	source = strings.TrimSpace(source)
	blocked := noDownloads && isURL(source)
	if blocked {
		source = "off"
	}
	f := feed{source: source, cacheName: cacheName, parse: parse}
	if off(source) {
		f.status = FeedStatus{State: StateDisabled, DownloadsOff: blocked}
	} else {
		f.status = FeedStatus{State: StateLoading, Origin: originOf(source, public)}
	}
	return f
}

func off(source string) bool {
	s := strings.TrimSpace(source)
	return s == "" || strings.EqualFold(s, "off")
}

func isURL(source string) bool {
	return strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://")
}

func originOf(source, public string) string {
	switch {
	case source == public:
		return OriginPublic
	case isURL(source):
		return OriginMirror
	}
	return OriginFile
}

// Snapshot returns the feeds as loaded so far, starting the loader on the
// first call.
func (s *Store) Snapshot() Snapshot {
	s.ensureStarted()
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := Snapshot{KEVStatus: s.kev.status, EPSSStatus: s.epss.status}
	snap.KEV, _ = s.kev.data.(*KEVCatalog)
	snap.EPSS, _ = s.epss.data.(*EPSSScores)
	now := time.Now()
	for _, st := range []*FeedStatus{&snap.KEVStatus, &snap.EPSSStatus} {
		st.Stale = st.State == StateReady && st.LoadedAt != nil && now.Sub(*st.LoadedAt) > StaleAfter
	}
	return snap
}

// WaitLoaded waits for the first load attempt, or ctx.
func (s *Store) WaitLoaded(ctx context.Context) {
	s.ensureStarted()
	select {
	case <-s.loaded:
	case <-ctx.Done():
	}
}

// Close stops the loader.
func (s *Store) Close() {
	s.start.Do(func() { close(s.done) })
	if s.stop != nil {
		s.stop()
		<-s.done
	}
}

func (s *Store) ensureStarted() {
	s.start.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.stop = cancel
		go s.run(ctx)
	})
}

func (s *Store) run(ctx context.Context) {
	defer close(s.done)
	feeds := []*feed{&s.kev, &s.epss}
	for _, f := range feeds {
		s.loadCached(f)
	}
	first := true
	for {
		var next time.Time
		for _, f := range feeds {
			if !f.on() {
				continue
			}
			if !time.Now().Before(f.due) {
				s.refreshOne(ctx, f)
			}
			if next.IsZero() || f.due.Before(next) {
				next = f.due
			}
		}
		if first {
			close(s.loaded)
			first = false
		}
		if next.IsZero() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		case asked := <-s.kick:
			// An attempt that ended after the request answers it: a reload
			// asked for while one runs, or as the first one starts, isn't
			// downloaded twice.
			for _, f := range feeds {
				if f.checked.Before(asked) {
					f.due = time.Time{}
				}
			}
		}
	}
}

func epssVersion(e *EPSSScores) string {
	if e.ScoreDate.IsZero() {
		return ""
	}
	return e.ScoreDate.UTC().Format("2006-01-02")
}

// refreshOne reloads a feed and schedules its next load.
func (s *Store) refreshOne(ctx context.Context, f *feed) {
	body, err := s.read(ctx, f.source)
	var data any
	var st FeedStatus
	if err == nil {
		data, st, err = f.parse(body)
	}
	now := time.Now()
	f.checked = now
	s.mu.Lock()
	f.status.CheckedAt = &now
	if err != nil {
		f.status.Error = err.Error()
		if f.data == nil {
			f.status.State = StateUnavailable
		}
		f.due = now.Add(s.cfg.RetryAfter)
	} else {
		st.State, st.Origin, st.LoadedAt, st.CheckedAt = StateReady, f.status.Origin, &now, &now
		f.status, f.data = st, data
		f.due = now.Add(s.cfg.Refresh)
	}
	s.mu.Unlock()
	if err == nil && s.cfg.CacheDir != "" && isURL(f.source) {
		if err := writeCache(filepath.Join(s.cfg.CacheDir, f.cacheName), body); err != nil {
			log.Printf("[vulnintel] Failed to keep a copy of %s for restarts: %v", f.cacheName, err)
		}
	}
}

// loadCached reads a URL feed's last good copy, so the page has it while
// the first fetch runs or when it fails. A copy younger than Refresh is
// used as it is: the next download is due when it would have been.
func (s *Store) loadCached(f *feed) {
	if s.cfg.CacheDir == "" || !isURL(f.source) {
		return
	}
	path := filepath.Join(s.cfg.CacheDir, f.cacheName)
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	data, st, err := f.parse(b)
	if err != nil {
		return
	}
	at := info.ModTime()
	if now := time.Now(); at.After(now) {
		at = now
	}
	s.mu.Lock()
	st.State, st.Origin, st.LoadedAt = StateReady, f.status.Origin, &at
	f.status, f.data = st, data
	s.mu.Unlock()
	f.due = at.Add(s.cfg.Refresh)
}

// writeCache replaces path atomically. Two Radars on one machine share the
// directory, so each writes its own temporary file.
func writeCache(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

func (s *Store) read(ctx context.Context, source string) ([]byte, error) {
	if !isURL(source) {
		path := strings.TrimPrefix(source, "file://")
		b, err := os.ReadFile(path)
		if err != nil {
			var pe *os.PathError
			if errors.As(err, &pe) {
				return nil, fmt.Errorf("couldn't read the feed file: %v", pe.Err)
			}
			return nil, errors.New("couldn't read the feed file")
		}
		return b, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, errors.New("the feed URL is not valid")
	}
	req.Header.Set("User-Agent", "Radar (https://github.com/skyhook-io/radar)")
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("couldn't reach the feed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the feed answered HTTP %d", resp.StatusCode)
	}
	b, err := readCapped(resp.Body, maxEPSSBytes)
	if err != nil {
		return nil, err
	}
	return b, nil
}
