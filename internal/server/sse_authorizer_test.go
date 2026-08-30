package server

import (
	"testing"
	"time"
)

// The SSE change authorizer must not re-issue a SAR for a repeated (verb, group,
// resource, namespace) within the TTL — otherwise a long-lived stream re-SARs
// every frame once the shared permission cache expires, serially in the single
// broadcast goroutine. It must re-check after the TTL so RBAC changes propagate.
func TestMemoizedAuthorizer(t *testing.T) {
	calls := 0
	// base allows only "list secrets"; counts invocations. All verdicts here are
	// authoritative (a real apiserver answer).
	base := func(group, resource, namespace, verb string) (bool, bool) {
		calls++
		return verb == "list" && resource == "secrets", true
	}
	clock := time.Now()
	ctxName := "cluster-a"
	authz := memoizedAuthorizer(base, 2*time.Minute, 10*time.Second, 0, func() string { return ctxName }, func() time.Time { return clock })

	if !authz("", "secrets", "team-a", "list") {
		t.Fatal("secrets/list should be allowed")
	}
	// Repeat within TTL → served from memo, no new base call.
	authz("", "secrets", "team-a", "list")
	authz("", "secrets", "team-a", "list")
	if calls != 1 {
		t.Fatalf("want 1 base call within TTL, got %d", calls)
	}

	// A different tuple is a distinct decision → one more base call.
	if authz("", "pods", "team-a", "list") {
		t.Fatal("pods/list should be denied by base")
	}
	if calls != 2 {
		t.Fatalf("want 2 base calls for a new tuple, got %d", calls)
	}

	// Past the TTL, the same tuple re-checks (propagates RBAC changes).
	clock = clock.Add(2*time.Minute + time.Second)
	authz("", "secrets", "team-a", "list")
	if calls != 3 {
		t.Fatalf("want a re-check after the TTL, got %d base calls", calls)
	}
}

// A kubeconfig context switch leaves SSE connections open, so the memo must not
// serve the previous cluster's decision for the new one — a changed context
// name is a cache miss even within the TTL, re-running the SAR against the new
// apiserver.
func TestMemoizedAuthorizer_ContextSwitchIsolatesDecisions(t *testing.T) {
	calls := 0
	// Same tuple resolves differently per cluster: allowed on cluster-a, denied
	// on cluster-b.
	var ctxName string
	base := func(group, resource, namespace, verb string) (bool, bool) {
		calls++
		return ctxName == "cluster-a", true
	}
	clock := time.Now()
	authz := memoizedAuthorizer(base, 2*time.Minute, 10*time.Second, 0, func() string { return ctxName }, func() time.Time { return clock })

	ctxName = "cluster-a"
	if !authz("", "secrets", "team-a", "list") {
		t.Fatal("secrets/list should be allowed on cluster-a")
	}
	// Switch context well within the TTL — the old allow must NOT leak.
	ctxName = "cluster-b"
	if authz("", "secrets", "team-a", "list") {
		t.Fatal("cluster-a's allow leaked into cluster-b within the TTL")
	}
	if calls != 2 {
		t.Fatalf("want a fresh base call after context switch, got %d", calls)
	}
}

// If a context switch lands while the SAR is in flight, the fresh result was
// decided against a different apiserver than the key names. The frame must fail
// closed (the verdict may reflect the wrong cluster), and the result must NOT be
// cached, or a switch-back within the TTL would serve the wrong cluster's
// decision.
func TestMemoizedAuthorizer_FailsClosedAndNoCacheWhenContextChangesDuringSAR(t *testing.T) {
	calls := 0
	ctxName := "cluster-a"
	base := func(group, resource, namespace, verb string) (bool, bool) {
		calls++
		if calls == 1 {
			// simulate a context switch landing mid-SAR
			ctxName = "cluster-b"
		}
		return true, true
	}
	clock := time.Now()
	authz := memoizedAuthorizer(base, 2*time.Minute, 10*time.Second, 0, func() string { return ctxName }, func() time.Time { return clock })

	// Key computed under cluster-a, but context flips to cluster-b during base.
	// The (possibly wrong-cluster) allow must not release the frame — fail closed.
	if authz("", "secrets", "team-a", "list") {
		t.Fatal("switch-straddling SAR must fail closed, not release the frame on the wrong cluster's verdict")
	}
	// Back on cluster-a within the TTL: a cached (poisoned) entry would be served
	// with no new base call. A fresh call proves it wasn't cached.
	ctxName = "cluster-a"
	authz("", "secrets", "team-a", "list")
	if calls != 2 {
		t.Fatalf("switch-straddling SAR must not be cached; want 2 base calls, got %d", calls)
	}
}

// A transient SAR failure (authoritative=false) fails closed for that frame and
// is cached only for the short negativeTTL — long enough that a degraded
// apiserver isn't re-SAR'd on every frame for the same tuple (which would stall
// the single broadcast goroutine), short enough that a momentary blip can't deny
// a readable tuple for the full TTL. Once negativeTTL elapses the tuple re-checks
// and a recovered apiserver's authoritative allow is cached for the full TTL.
func TestMemoizedAuthorizer_TransientFailureCachedBriefly(t *testing.T) {
	calls := 0
	// First call is a transient failure (deny, non-authoritative); afterwards the
	// apiserver recovers and authoritatively allows.
	base := func(group, resource, namespace, verb string) (bool, bool) {
		calls++
		if calls == 1 {
			return false, false // transient failure: fail-closed, cache only briefly
		}
		return true, true // recovered: authoritative allow
	}
	clock := time.Now()
	ctxName := "cluster-a"
	negativeTTL := 10 * time.Second
	authz := memoizedAuthorizer(base, 2*time.Minute, negativeTTL, 0, func() string { return ctxName }, func() time.Time { return clock })

	if authz("", "secrets", "team-a", "list") {
		t.Fatal("transient failure must fail closed (deny) for this frame")
	}
	// Same tuple within the negativeTTL: served from the brief negative cache, so
	// the degraded apiserver is NOT re-SAR'd — no new base call.
	if authz("", "secrets", "team-a", "list") {
		t.Fatal("negative cache must keep failing closed within negativeTTL")
	}
	if calls != 1 {
		t.Fatalf("transient failure should be cached for negativeTTL; want 1 base call, got %d", calls)
	}

	// Past the negativeTTL the tuple re-checks; the apiserver has recovered.
	clock = clock.Add(negativeTTL + time.Second)
	if !authz("", "secrets", "team-a", "list") {
		t.Fatal("after negativeTTL the recovered apiserver should allow")
	}
	if calls != 2 {
		t.Fatalf("want a re-check after negativeTTL, got %d base calls", calls)
	}
	// The authoritative allow IS cached for the full TTL — a further call within it
	// is served from memo.
	clock = clock.Add(negativeTTL + time.Second) // still well within the 2m TTL
	authz("", "secrets", "team-a", "list")
	if calls != 2 {
		t.Fatalf("authoritative allow should be cached for the full TTL; want still 2 base calls, got %d", calls)
	}
}

// sweepExpiredAuthMemo reclaims only the entries whose TTL has elapsed, leaving
// live ones intact — the memory bound for a long-lived all-namespace stream that
// would otherwise accumulate expired entries for every observed tuple.
func TestSweepExpiredAuthMemo(t *testing.T) {
	base := time.Now()
	memo := map[string]authMemoEntry{
		"expired-a": {allowed: true, expires: base.Add(-time.Second)},
		"expired-b": {allowed: false, expires: base}, // expires == now → expired
		"live":      {allowed: true, expires: base.Add(time.Minute)},
	}
	removed := sweepExpiredAuthMemo(memo, base)
	if removed != 2 {
		t.Fatalf("want 2 expired entries removed, got %d", removed)
	}
	if _, ok := memo["live"]; !ok || len(memo) != 1 {
		t.Fatalf("sweep must keep only the live entry, got %v", memo)
	}
}

// nil user (auth off) is a strict passthrough — every frame is allowed and no
// SAR is ever issued.
func TestNewSSEChangeAuthorizer_AuthOff(t *testing.T) {
	s := &Server{}
	authz := s.newSSEChangeAuthorizer(nil, nil)
	if !authz("", "secrets", "team-a", "list") {
		t.Fatal("auth-off authorizer must allow everything")
	}
}
