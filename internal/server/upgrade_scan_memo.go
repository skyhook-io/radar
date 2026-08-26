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
	now        func() time.Time
}

func newUpgradeScanMemo() *upgradeScanMemo {
	return &upgradeScanMemo{
		entries:  map[string]*upgradeScanEntry{},
		ttl:      upgradeScanMemoTTL,
		cooldown: upgradeScanRefreshCooldown,
		now:      time.Now,
	}
}

var (
	upgradeScanMemoSingleton     *upgradeScanMemo
	upgradeScanMemoSingletonOnce sync.Once
)

func getUpgradeScanMemo() *upgradeScanMemo {
	upgradeScanMemoSingletonOnce.Do(func() {
		upgradeScanMemoSingleton = newUpgradeScanMemo()
		k8s.OnContextSwitch(func(string) {
			upgradeScanMemoSingleton.invalidate()
		})
	})
	return upgradeScanMemoSingleton
}

func (m *upgradeScanMemo) invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	m.entries = map[string]*upgradeScanEntry{}
}

// upgradeScanKey distinguishes a nil (cluster-wide) ceiling from an empty
// (no-access) one, and is order-insensitive in the namespace list.
func upgradeScanKey(username, target string, namespaces []string) string {
	scope := "\x02cluster-wide"
	if namespaces != nil {
		sorted := append([]string(nil), namespaces...)
		slices.Sort(sorted)
		scope = strings.Join(sorted, "\x01")
	}
	return username + "\x00" + target + "\x00" + scope
}

func newUpgradeScanID() string {
	raw := make([]byte, 8)
	// crypto/rand.Read cannot fail as of Go 1.24 — it aborts the process
	// instead of returning an error, so there is no fallback path that could
	// hand two snapshots the same id.
	_, _ = rand.Read(raw)
	return "sc_" + hex.EncodeToString(raw)
}

func (m *upgradeScanMemo) get(ctx context.Context, key string, refresh bool, scan func(context.Context) (*upgradereadiness.ScanResults, error)) (UpgradeScanOutcome, error) {
	m.mu.Lock()
	gen := m.generation
	now := m.now()
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
	m.mu.Unlock()

	results, err := scan(ctx)

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
	if k8s.GetResourceCache() == nil {
		return UpgradeScanOutcome{}, ErrUpgradeScanNotReady
	}
	currentVersion := k8s.GetServerVersion()
	targetVersion, err := upgradereadiness.EffectiveTarget(currentVersion, targetVersion)
	if err != nil {
		return UpgradeScanOutcome{}, err
	}
	username := ""
	if user := auth.UserFromContext(ctx); user != nil {
		username = user.Username
	}
	namespaces := authz.Namespaces()
	key := upgradeScanKey(username, targetVersion, namespaces)
	return getUpgradeScanMemo().get(ctx, key, refresh, func(ctx context.Context) (*upgradereadiness.ScanResults, error) {
		return runUpgradeReadinessScan(ctx, authz, namespaces, currentVersion, targetVersion)
	})
}
