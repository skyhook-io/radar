package vulnintel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
type Store struct {
	cfg   Config
	start sync.Once
	stop  context.CancelFunc
	done  chan struct{}

	mu   sync.RWMutex
	snap Snapshot
	// loaded fires after the first load attempt of both feeds.
	loaded chan struct{}

	// kick asks the loader to reload now (RefreshNow); lastManual is when
	// someone last did, for the rate limit.
	kick       chan struct{}
	manualMu   sync.Mutex
	lastManual time.Time
	clock      func() time.Time
}

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
	if off(s.cfg.KEVSource) && off(s.cfg.EPSSSource) {
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
	case s.kick <- struct{}{}:
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
	kevBlocked := cfg.NoDownloads && isURL(cfg.KEVSource)
	epssBlocked := cfg.NoDownloads && isURL(cfg.EPSSSource)
	if kevBlocked {
		cfg.KEVSource = "off"
	}
	if epssBlocked {
		cfg.EPSSSource = "off"
	}
	s := &Store{cfg: cfg, loaded: make(chan struct{}), done: make(chan struct{}), kick: make(chan struct{}, 1), clock: time.Now}
	s.snap.KEVStatus = initialStatus(cfg.KEVSource, DefaultKEVURL)
	s.snap.EPSSStatus = initialStatus(cfg.EPSSSource, DefaultEPSSURL)
	s.snap.KEVStatus.DownloadsOff = kevBlocked
	s.snap.EPSSStatus.DownloadsOff = epssBlocked
	return s
}

func initialStatus(source, public string) FeedStatus {
	if off(source) {
		return FeedStatus{State: StateDisabled}
	}
	return FeedStatus{State: StateLoading, Origin: originOf(source, public)}
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
	return s.snap
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
	if s.cfg.CacheDir != "" {
		s.loadCached()
	}
	first := true
	for {
		ok := s.refresh(ctx)
		if first {
			close(s.loaded)
			first = false
		}
		wait := s.cfg.Refresh
		if !ok {
			wait = s.cfg.RetryAfter
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-s.kick:
		}
	}
}

// refresh reloads both feeds; ok is false when one failed.
func (s *Store) refresh(ctx context.Context) bool {
	okK := s.refreshOne(ctx, s.cfg.KEVSource, "kev.json", func(b []byte) (any, FeedStatus, error) {
		c, err := ParseKEV(bytes.NewReader(b))
		if err != nil {
			return nil, FeedStatus{}, err
		}
		return c, FeedStatus{Version: c.Version, Count: c.Len()}, nil
	}, func(v any, st FeedStatus) { s.snap.KEV, s.snap.KEVStatus = asKEV(v), st }, func() *FeedStatus { return &s.snap.KEVStatus })
	okE := s.refreshOne(ctx, s.cfg.EPSSSource, "epss.csv.gz", func(b []byte) (any, FeedStatus, error) {
		e, err := ParseEPSS(bytes.NewReader(b))
		if err != nil {
			return nil, FeedStatus{}, err
		}
		return e, FeedStatus{Version: epssVersion(e), Count: e.Len()}, nil
	}, func(v any, st FeedStatus) { s.snap.EPSS, s.snap.EPSSStatus = asEPSS(v), st }, func() *FeedStatus { return &s.snap.EPSSStatus })
	return okK && okE
}

func asKEV(v any) *KEVCatalog {
	c, _ := v.(*KEVCatalog)
	return c
}

func asEPSS(v any) *EPSSScores {
	e, _ := v.(*EPSSScores)
	return e
}

func epssVersion(e *EPSSScores) string {
	if e.ScoreDate.IsZero() {
		return ""
	}
	return e.ScoreDate.UTC().Format("2006-01-02")
}

func (s *Store) refreshOne(ctx context.Context, source, cacheName string, parse func([]byte) (any, FeedStatus, error), set func(any, FeedStatus), current func() *FeedStatus) bool {
	if off(source) {
		return true
	}
	body, err := s.read(ctx, source)
	var parsed any
	var st FeedStatus
	if err == nil {
		parsed, st, err = parse(body)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := current()
	checked := time.Now()
	cur.CheckedAt = &checked
	if err != nil {
		cur.Error = err.Error()
		if cur.LoadedAt == nil {
			cur.State = StateUnavailable
		} else {
			cur.Stale = time.Since(*cur.LoadedAt) > StaleAfter
		}
		return false
	}
	st.State, st.Origin, st.LoadedAt, st.CheckedAt = StateReady, cur.Origin, &checked, &checked
	set(parsed, st)
	if s.cfg.CacheDir != "" && isURL(source) {
		writeCache(filepath.Join(s.cfg.CacheDir, cacheName), body)
	}
	return true
}

// loadCached reads the last good copies, so the page has them while the
// first fetch runs or when it fails.
func (s *Store) loadCached() {
	for _, f := range []struct {
		source, name string
		load         func([]byte, time.Time)
	}{
		{s.cfg.KEVSource, "kev.json", func(b []byte, at time.Time) {
			if c, err := ParseKEV(bytes.NewReader(b)); err == nil {
				s.snap.KEV = c
				s.snap.KEVStatus = FeedStatus{State: StateReady, Origin: s.snap.KEVStatus.Origin, Version: c.Version, Count: c.Len(), LoadedAt: &at}
			}
		}},
		{s.cfg.EPSSSource, "epss.csv.gz", func(b []byte, at time.Time) {
			if e, err := ParseEPSS(bytes.NewReader(b)); err == nil {
				s.snap.EPSS = e
				s.snap.EPSSStatus = FeedStatus{State: StateReady, Origin: s.snap.EPSSStatus.Origin, Version: epssVersion(e), Count: e.Len(), LoadedAt: &at}
			}
		}},
	} {
		if off(f.source) || !isURL(f.source) {
			continue
		}
		path := filepath.Join(s.cfg.CacheDir, f.name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s.mu.Lock()
		f.load(b, info.ModTime())
		s.mu.Unlock()
	}
}

func writeCache(path string, body []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
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
