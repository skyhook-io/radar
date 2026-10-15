package cnpg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// cnpgRuntimeRunner runs proxy reads with bounded concurrency under the
// request's context.
type cnpgRuntimeRunner struct {
	ctx context.Context
	sem chan struct{}
	wg  sync.WaitGroup
}

func newCNPGRuntimeRunner(ctx context.Context) *cnpgRuntimeRunner {
	return &cnpgRuntimeRunner{ctx: ctx, sem: make(chan struct{}, cnpgRuntimeConcurrency)}
}

func (r *cnpgRuntimeRunner) do(fn func(ctx context.Context)) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		select {
		case r.sem <- struct{}{}:
			defer func() { <-r.sem }()
		case <-r.ctx.Done():
			// The caller is gone and nobody reads this result. Running fn here
			// would bypass the cap: memoized reads outlive the caller, so every
			// queued read would start at once.
			return
		}
		// select picks at random when both are ready: a free slot does not
		// make a cancelled caller's read worth starting.
		if r.ctx.Err() != nil {
			return
		}
		fn(r.ctx)
	}()
}

func (r *cnpgRuntimeRunner) wait() { r.wg.Wait() }

// The memo collapses repeated reads of one endpoint by one identity — several
// viewers, a fast refresh — into one scrape per lifetime. The key includes the
// kube context and the Pod UID, so a context switch or a recreated Pod never
// serves a stale answer, and the identity, so one caller's answer never
// reaches another.
var (
	cnpgRuntimeMemoMu      sync.Mutex
	cnpgRuntimeMemoEntries = map[string]cnpgRuntimeMemoEntry{}
	cnpgRuntimeMemoGroup   singleflight.Group
)

const cnpgRuntimeMemoMaxEntries = 4096

// Two proxied requests (a scheme fallback) plus slack.
const cnpgMemoizedReadTimeout = 2*cnpgRuntimeRequestTimeout + time.Second

type cnpgRuntimeMemoEntry struct {
	value   any
	expires time.Time
}

func memoized[T any](ctx context.Context, identity string, target proxyTarget, ttl time.Duration, fetch func(context.Context) T) T {
	key := fmt.Sprintf("%s\x00%s/%s\x00%s\x00%s:%d%s", identity, target.namespace, target.pod, target.podUID, target.scheme, target.port, target.path)
	now := time.Now()
	cnpgRuntimeMemoMu.Lock()
	if e, ok := cnpgRuntimeMemoEntries[key]; ok && now.Before(e.expires) {
		cnpgRuntimeMemoMu.Unlock()
		return e.value.(T)
	}
	cnpgRuntimeMemoMu.Unlock()

	v, _, _ := cnpgRuntimeMemoGroup.Do(key, func() (any, error) {
		// Every caller waiting on this key shares the one read, so it runs
		// detached from whichever caller started it: that caller hanging up
		// must not fail the others. Values (identity) are kept; the deadline
		// covers a scheme fallback's second request.
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cnpgMemoizedReadTimeout)
		defer cancel()
		timedOut := new(atomic.Bool)
		got := fetch(context.WithValue(readCtx, cnpgReadTimedOutKey{}, timedOut))
		// A read that ran out of time (its own deadline or a request's inside
		// it) says nothing lasting about the Pod: the next caller tries again.
		if readCtx.Err() == nil && !timedOut.Load() {
			cnpgRuntimeMemoMu.Lock()
			if len(cnpgRuntimeMemoEntries) >= cnpgRuntimeMemoMaxEntries {
				pruneCNPGRuntimeMemoLocked(time.Now())
			}
			if len(cnpgRuntimeMemoEntries) < cnpgRuntimeMemoMaxEntries {
				cnpgRuntimeMemoEntries[key] = cnpgRuntimeMemoEntry{value: got, expires: time.Now().Add(ttl)}
			}
			cnpgRuntimeMemoMu.Unlock()
		}
		return got, nil
	})
	return v.(T)
}

func pruneCNPGRuntimeMemoLocked(now time.Time) {
	for k, e := range cnpgRuntimeMemoEntries {
		if !now.Before(e.expires) {
			delete(cnpgRuntimeMemoEntries, k)
		}
	}
}

// classifyCNPGProxyFailure classifies a failed read and, for a failure in
// transit, replaces the raw error with a sentence; the raw error is logged.
// cnpgReadTimedOutKey carries a flag a memoized read sets when any request
// inside it timed out, so the memo does not keep a timeout for its full TTL.
type cnpgReadTimedOutKey struct{}

func cnpgMarkTimedOut(ctx context.Context, err error) {
	flag, _ := ctx.Value(cnpgReadTimedOutKey{}).(*atomic.Bool)
	if flag == nil {
		return
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) ||
		cnpgRelayedTransportTimeout(err) {
		flag.Store(true)
	}
}
