package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

const (
	// upgradeScanMemoTTL absorbs an agent's follow-up check expansions of one
	// scan; fix-then-rescan loops use refresh instead of waiting it out.
	upgradeScanMemoTTL = 60 * time.Second
	// upgradeScanRefreshCooldown bounds how often refresh can force a full
	// live collection per key — within it, refresh returns the newest
	// completed scan. Enforced control, not description guidance.
	upgradeScanRefreshCooldown = 5 * time.Second
	// upgradeScanMemoMaxEntries bounds memo cardinality: the key contains the
	// caller-controlled target (any forward minor validates), so without a cap
	// a caller iterating targets would retain one full ScanResults per key.
	// Completed entries beyond the cap are evicted earliest-expiry-first;
	// in-flight entries are never evicted.
	upgradeScanMemoMaxEntries = 32
	// upgradeScanMaxConcurrent bounds live collections across ALL keys —
	// single-flight only dedupes within one key, and distinct targets are
	// distinct keys. Excess leaders wait; the pre-memo endpoint had no bound
	// at all, so this is strictly tighter than the status quo.
	upgradeScanMaxConcurrent = 2
)

var (
	// ErrUpgradeScanStaleContext is returned when the kubeconfig context
	// changed between a scan starting (or a memo entry being written) and the
	// result being returned. A scan whose collectors straddle a context switch
	// can contain mixed-cluster evidence — failing is safer than serving it.
	ErrUpgradeScanStaleContext = errors.New("cluster context changed during the scan — retry the request")
	// ErrUpgradeScanNotReady mirrors the cache-not-initialized 503.
	ErrUpgradeScanNotReady = errors.New("cluster cache not initialized")
)

// UpgradeScanOutcome is one served scan snapshot. ObservedAt is when the
// underlying scan started — a memoized outcome keeps the original stamp so
// staleness stays visible. ScanID identifies the snapshot for paging
// consumers.
type UpgradeScanOutcome struct {
	Results    *upgradereadiness.ScanResults
	ObservedAt time.Time
	ScanID     string
	FromCache  bool
}

type upgradeScanEntry struct {
	done       chan struct{}
	generation uint64
	startedAt  time.Time
	// Written by the leader before done closes:
	results     *upgradereadiness.ScanResults
	err         error
	scanID      string
	completedAt time.Time
	expiresAt   time.Time
}

// upgradeScanMemo memoizes completed ScanResults per (identity, target,
// namespace-ceiling) with single-flight per key: concurrent callers — refresh
// included — join the in-flight scan rather than fanning out live collections.
// The cluster-context generation is part of every entry and revalidated before
// any result is returned, not just at insertion: invalidation alone would let
// a racing read serve the previous cluster's scan.
type upgradeScanMemo struct {
	mu         sync.Mutex
	generation uint64
	entries    map[string]*upgradeScanEntry
	ttl        time.Duration
	cooldown   time.Duration
	maxEntries int
	scanSlots  chan struct{}
	now        func() time.Time
}

func newUpgradeScanMemo() *upgradeScanMemo {
	return &upgradeScanMemo{
		entries:    map[string]*upgradeScanEntry{},
		ttl:        upgradeScanMemoTTL,
		cooldown:   upgradeScanRefreshCooldown,
		maxEntries: upgradeScanMemoMaxEntries,
		scanSlots:  make(chan struct{}, upgradeScanMaxConcurrent),
		now:        time.Now,
	}
}

var (
	upgradeScanMemoSingleton     *upgradeScanMemo
	upgradeScanMemoSingletonOnce sync.Once
)

func getUpgradeScanMemo() *upgradeScanMemo {
	upgradeScanMemoSingletonOnce.Do(func() {
		upgradeScanMemoSingleton = newUpgradeScanMemo()
		// Bump at switch START (old context still active) and again at
		// completion: a scan straddling any part of the switch window sees a
		// generation change and is refused, instead of racing the swap of the
		// global clients and cache mid-collection.
		k8s.OnBeforeContextSwitch(func(string) {
			upgradeScanMemoSingleton.invalidate()
		})
		k8s.OnContextSwitch(func(string) {
			upgradeScanMemoSingleton.invalidate()
		})
	})
	return upgradeScanMemoSingleton
}

func (m *upgradeScanMemo) currentGeneration() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.generation
}

func (m *upgradeScanMemo) invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	m.entries = map[string]*upgradeScanEntry{}
}

// upgradeScanKey distinguishes a nil (cluster-wide) ceiling from an empty
// (no-access) one, and is order-insensitive in namespaces and groups. Groups
// are part of the identity even though the SAR caches key on username alone:
// per-kind grants can differ between two requests carrying the same username
// with different group sets (forwarded-identity setups), and a whole scan is a
// far bigger privileged blob than one cached SAR verdict.
func upgradeScanKey(username string, groups []string, target string, namespaces []string) string {
	sortedGroups := append([]string(nil), groups...)
	slices.Sort(sortedGroups)
	scope := "\x02cluster-wide"
	if namespaces != nil {
		sorted := append([]string(nil), namespaces...)
		slices.Sort(sorted)
		scope = strings.Join(sorted, "\x01")
	}
	return username + "\x00" + strings.Join(sortedGroups, "\x01") + "\x00" + target + "\x00" + scope
}

func newUpgradeScanID() string {
	raw := make([]byte, 8)
	// crypto/rand.Read cannot fail as of Go 1.24 — it aborts the process
	// instead of returning an error, so there is no fallback path that could
	// hand two snapshots the same id.
	_, _ = rand.Read(raw)
	return "sc_" + hex.EncodeToString(raw)
}

// get serves the memo under the caller's captured generation. gen must be
// read via currentGeneration BEFORE any cluster-dependent input (cache,
// server version, namespace ceiling) was resolved — a switch completing
// between those reads and this call is then caught here as a mismatch instead
// of caching inputs from one cluster under the other's generation.
func (m *upgradeScanMemo) get(ctx context.Context, key string, refresh bool, gen uint64, scan func(context.Context) (*upgradereadiness.ScanResults, error)) (UpgradeScanOutcome, error) {
	m.mu.Lock()
	if gen != m.generation {
		m.mu.Unlock()
		return UpgradeScanOutcome{}, ErrUpgradeScanStaleContext
	}
	now := m.now()
	m.sweepLocked(now)
	if entry := m.entries[key]; entry != nil {
		select {
		case <-entry.done:
			fresh := entry.err == nil && entry.generation == gen && now.Before(entry.expiresAt)
			if fresh && (!refresh || now.Sub(entry.completedAt) < m.cooldown) {
				out := m.outcomeLocked(entry, true)
				m.mu.Unlock()
				return out, nil
			}
			// Expired, stale-generation, or refresh past the cooldown:
			// fall through and become the leader of a new scan.
		default:
			// In-flight: join it. Refresh joins too — a scan already running
			// is at least as fresh as one started now.
			m.mu.Unlock()
			return m.waitForEntry(ctx, entry)
		}
	}

	leader := &upgradeScanEntry{done: make(chan struct{}), generation: gen, startedAt: now}
	m.entries[key] = leader
	m.evictOverCapLocked()
	m.mu.Unlock()

	results, err := m.runBoundedScan(ctx, scan)

	m.mu.Lock()
	completed := m.now()
	if err == nil && m.generation != leader.generation {
		// The context switched while collectors ran; the evidence may mix
		// clusters. Refuse to serve or cache it.
		err = ErrUpgradeScanStaleContext
	}
	leader.err = err
	if err == nil {
		leader.results = results
		leader.scanID = newUpgradeScanID()
		leader.completedAt = completed
		leader.expiresAt = completed.Add(m.ttl)
	} else if m.entries[key] == leader {
		// Failed and stale scans are not cached — waiters get the error, the
		// next caller scans fresh.
		delete(m.entries, key)
	}
	out := m.outcomeLocked(leader, false)
	close(leader.done)
	m.mu.Unlock()

	if err != nil {
		return UpgradeScanOutcome{}, err
	}
	return out, nil
}

// sweepLocked drops completed entries that have expired or belong to a
// previous generation — without it, distinct keys (the target is
// caller-controlled) would retain their ScanResults until the next context
// switch. In-flight entries are left for their leaders to settle.
func (m *upgradeScanMemo) sweepLocked(now time.Time) {
	for key, entry := range m.entries {
		select {
		case <-entry.done:
			if entry.err != nil || entry.generation != m.generation || !now.Before(entry.expiresAt) {
				delete(m.entries, key)
			}
		default:
		}
	}
}

// evictOverCapLocked bounds memo cardinality by dropping completed entries
// earliest-expiry-first. In-flight entries are never evicted — their leaders
// hold the done channel — but they carry no results, so their footprint is
// small and their count is bounded by the scan-slot queue in practice.
func (m *upgradeScanMemo) evictOverCapLocked() {
	for len(m.entries) > m.maxEntries {
		victimKey := ""
		var victim *upgradeScanEntry
		for key, entry := range m.entries {
			select {
			case <-entry.done:
				if victim == nil || entry.expiresAt.Before(victim.expiresAt) {
					victimKey, victim = key, entry
				}
			default:
			}
		}
		if victim == nil {
			return
		}
		delete(m.entries, victimKey)
	}
}

// runBoundedScan gates the live collection behind the global concurrency
// slots. Single-flight already dedupes per key; this bounds distinct keys
// (distinct targets) from fanning out unbounded concurrent collections.
func (m *upgradeScanMemo) runBoundedScan(ctx context.Context, scan func(context.Context) (*upgradereadiness.ScanResults, error)) (*upgradereadiness.ScanResults, error) {
	select {
	case m.scanSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-m.scanSlots }()
	return scan(ctx)
}

// waitForEntry blocks until the in-flight entry completes, then revalidates
// the context generation before returning its result.
func (m *upgradeScanMemo) waitForEntry(ctx context.Context, entry *upgradeScanEntry) (UpgradeScanOutcome, error) {
	select {
	case <-entry.done:
	case <-ctx.Done():
		return UpgradeScanOutcome{}, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.err != nil {
		return UpgradeScanOutcome{}, entry.err
	}
	if entry.generation != m.generation {
		return UpgradeScanOutcome{}, ErrUpgradeScanStaleContext
	}
	return m.outcomeLocked(entry, true), nil
}

func (m *upgradeScanMemo) outcomeLocked(entry *upgradeScanEntry, fromCache bool) UpgradeScanOutcome {
	return UpgradeScanOutcome{
		Results:    entry.results,
		ObservedAt: entry.startedAt,
		ScanID:     entry.scanID,
		FromCache:  fromCache,
	}
}

// RunUpgradeReadinessScanMemoized validates the target and serves the scan
// through the shared identity-scoped memo. The HTTP handler and the MCP tool
// both call this, so an agent's tier-2 expansions hit the tier-1 call's scan
// and a user's ordinary UI refetch within the TTL is free. Callers map the
// sentinel errors (upgradereadiness.ErrInvalidTargetVersion / ErrNonForwardTarget /
// ErrInvalidCurrentVersion, ErrUpgradeScanNotReady, ErrUpgradeScanStaleContext)
// to their surface's error shape.
//
// Results are shared across callers of the same key — treat the ScanResults
// as immutable; shape copies, never mutate in place.
func RunUpgradeReadinessScanMemoized(ctx context.Context, authz UpgradeEvidenceAuthorizer, targetVersion string, refresh bool) (UpgradeScanOutcome, error) {
	// The generation is captured BEFORE any cluster-dependent read below
	// (cache, server version, namespace ceiling): a context switch completing
	// after this line is caught as a mismatch in get, so inputs resolved
	// against one cluster can never be cached or served under the other's
	// generation.
	memo := getUpgradeScanMemo()
	gen := memo.currentGeneration()
	if k8s.GetResourceCache() == nil {
		return UpgradeScanOutcome{}, ErrUpgradeScanNotReady
	}
	currentVersion := k8s.GetServerVersion()
	targetVersion, err := upgradereadiness.EffectiveTarget(currentVersion, targetVersion)
	if err != nil {
		return UpgradeScanOutcome{}, err
	}
	username, groups := "", []string(nil)
	if user := auth.UserFromContext(ctx); user != nil {
		username, groups = user.Username, user.Groups
	}
	namespaces := authz.Namespaces()
	key := upgradeScanKey(username, groups, targetVersion, namespaces)
	return memo.get(ctx, key, refresh, gen, func(ctx context.Context) (*upgradereadiness.ScanResults, error) {
		return runUpgradeReadinessScan(ctx, authz, namespaces, currentVersion, targetVersion)
	})
}
