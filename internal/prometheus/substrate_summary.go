package prometheus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/substrate"
)

// The Agent Substrate workspace asks for the pool and node verdicts of every
// pool and worker node the caller may read, and polls. Collecting one runs
// that pool's or node's chart queries, which takes seconds, so the workspace
// never waits for it: each subject's answer is cached on its own and reused
// for substrateSummaryTTL, a stale one is served while a fresh one is
// collected in the background, and a subject with none yet reads unknown.
// Callers with different grants ask for different subject sets; caching per
// subject lets overlapping sets share the work, and each caller is answered
// from the subjects it asked for. At most substrateSummaryParallel subjects
// are collected at a time per Prometheus connection, across all callers, and
// a subject not started within substrateSummaryBudget reads unknown.
const (
	substrateSummaryTTL      = 30 * time.Second
	substrateSummaryParallel = 4
	substrateSummaryBudget   = 60 * time.Second
	substrateSummaryRange    = "1h"
	// substrateSummaryKeep bounds how long an unused answer is kept.
	substrateSummaryKeep = 10 * time.Minute
)

// substrateSubjectEntry is one pool's or node's answer.
type substrateSubjectEntry struct {
	// client is the connection the answer came from. Keeping it in the entry
	// keeps its address, part of the key, from being reused by a later
	// client while the entry lives.
	client *Client
	at     time.Time
	pool   substrate.PoolService
	node   substrate.NodeService
}

var (
	substrateSummaryMu       sync.Mutex
	substrateSummaryCache    = map[string]substrateSubjectEntry{}
	substrateSummaryInFlight = map[string]bool{}
	// substrateSummarySlots bounds collection per connection, by connection
	// key.
	substrateSummarySlots = map[string]chan struct{}{}
)

// Seams for tests: collecting one subject, and whether an answer collected
// under a connection may still be published.
var (
	substrateCollectPool = func(ctx context.Context, client *Client, p substrate.PoolRef) substrate.PoolService {
		start, end, step := alignSubstrateRange(parseTimeRange(substrateSummaryRange))
		return poolServiceOf(collectSubstratePool(ctx, client, clientScopeResolver(client), p.Namespace, p.Name, substrateSummaryRange, start, end, step))
	}
	substrateCollectNode = func(ctx context.Context, client *Client, node string) substrate.NodeService {
		start, end, step := alignSubstrateRange(parseTimeRange(substrateSummaryRange))
		return nodeServiceOf(collectSubstrateNode(ctx, client, clientScopeResolver(client), node, substrateSummaryRange, start, end, step))
	}
	substrateStillConnected = func(client *Client, generation, epoch uint64) bool {
		return client == GetClient() && generation == client.DiscoveryGeneration() && epoch == client.backendEpoch()
	}
)

// SubstrateService reads Substrate's pool verdicts (capacity, resume p95,
// failed resumes and crashes over the last hour) and node verdicts (overdue
// uploads, oldest unsaved state) for the given subjects: the same readings
// the WorkerPool and Node pages show. The caller must already be allowed to
// read each pool and node it passes. Service.Read is false, with Why, when
// Prometheus is not connected or no subject's first answer is collected yet.
func SubstrateService(ctx context.Context, pools []substrate.PoolRef, nodes []string) *substrate.Service {
	client, err := ClientForOperation()
	if err != nil {
		why := "Prometheus is not connected"
		if !errors.Is(err, ErrPrometheusUnavailable) {
			why = "Prometheus is not usable: " + truncate(err.Error(), 200)
		}
		return &substrate.Service{Why: why}
	}
	return substrateServiceFor(client, client.DiscoveryGeneration(), client.backendEpoch(), pools, nodes)
}

const substrateCollecting = "Radar is collecting Substrate's metrics from Prometheus; they show on the next refresh"

func substrateServiceFor(client *Client, generation, epoch uint64, pools []substrate.PoolRef, nodes []string) *substrate.Service {
	conn := fmt.Sprintf("%p\x00%d\x00%d", client, generation, epoch)
	svc := &substrate.Service{Read: true, Window: substrateSummaryRange, Pools: map[string]substrate.PoolService{}, Nodes: map[string]substrate.NodeService{}}
	answered := 0

	substrateSummaryMu.Lock()
	defer substrateSummaryMu.Unlock()
	use := func(key string, collect func(context.Context) substrateSubjectEntry) (substrateSubjectEntry, bool) {
		e, ok := substrateSummaryCache[key]
		ok = ok && e.client == client
		if (!ok || time.Since(e.at) >= substrateSummaryTTL) && !substrateSummaryInFlight[key] {
			substrateSummaryInFlight[key] = true
			collectSubstrateSubject(client, generation, epoch, conn, key, collect)
		}
		if ok {
			answered++
			if svc.ReadAt.IsZero() || e.at.Before(svc.ReadAt) {
				svc.ReadAt = e.at
			}
		}
		return e, ok
	}
	for _, p := range pools {
		e, ok := use(conn+"\x00p:"+p.Key(), func(ctx context.Context) substrateSubjectEntry {
			return substrateSubjectEntry{pool: substrateCollectPool(ctx, client, p)}
		})
		if !ok {
			e.pool = substrate.PoolService{Verdict: substrate.ServiceUnknown, Reason: substrateCollecting}
		}
		svc.Pools[p.Key()] = e.pool
	}
	for _, n := range nodes {
		e, ok := use(conn+"\x00n:"+n, func(ctx context.Context) substrateSubjectEntry {
			return substrateSubjectEntry{node: substrateCollectNode(ctx, client, n)}
		})
		if !ok {
			e.node = substrate.NodeService{Verdict: substrate.ServiceUnknown, Reason: substrateCollecting}
		}
		svc.Nodes[n] = e.node
	}
	if answered == 0 && len(pools)+len(nodes) > 0 {
		return &substrate.Service{Why: substrateCollecting, Collecting: true}
	}
	return svc
}

// collectSubstrateSubject collects one subject under its connection's bound and
// publishes the answer unless the connection changed meanwhile. Called with
// substrateSummaryMu held; it runs on its own.
func collectSubstrateSubject(client *Client, generation, epoch uint64, conn, key string, collect func(context.Context) substrateSubjectEntry) {
	slots := substrateSummarySlots[conn]
	if slots == nil {
		slots = make(chan struct{}, substrateSummaryParallel)
		substrateSummarySlots[conn] = slots
	}
	ctx, cancel := context.WithTimeout(context.Background(), substrateSummaryBudget)
	go func() {
		defer cancel()
		var e substrateSubjectEntry
		select {
		case slots <- struct{}{}:
			e = collect(ctx)
			<-slots
		case <-ctx.Done():
			e = collect(ctx) // records unknown from the expired context
		}
		e.client, e.at = client, time.Now()

		substrateSummaryMu.Lock()
		defer substrateSummaryMu.Unlock()
		delete(substrateSummaryInFlight, key)
		for k, old := range substrateSummaryCache {
			if time.Since(old.at) > substrateSummaryKeep {
				delete(substrateSummaryCache, k)
			}
		}
		// An answer collected across a connection change is another
		// backend's: drop it.
		if substrateStillConnected(client, generation, epoch) {
			substrateSummaryCache[key] = e
			return
		}
		for k := range substrateSummaryInFlight {
			if strings.HasPrefix(k, conn+"\x00") {
				return
			}
		}
		delete(substrateSummarySlots, conn)
	}()
}

func factValue(facts []SubstrateFact, id string) (*float64, *float64) {
	for _, f := range facts {
		if f.ID == id && f.State == SubstrateFactOK {
			return f.Value, f.Of
		}
	}
	return nil, nil
}

// noVerdictReason says why a response carries no verdict.
func noVerdictReason(resp SubstrateHealthResponse) string {
	if resp.Reason != "" {
		return resp.Reason
	}
	if resp.Status == SubstrateNotFound {
		return "Prometheus holds none of Substrate's series for it"
	}
	return "Prometheus gave no verdict (" + resp.Status + ")"
}

func poolServiceOf(resp SubstrateHealthResponse) substrate.PoolService {
	if resp.Verdict == nil {
		return substrate.PoolService{Verdict: substrate.ServiceUnknown, Reason: noVerdictReason(resp)}
	}
	v := resp.Verdict
	ps := substrate.PoolService{Verdict: v.State, Reason: v.Reason}
	ps.ResumeP95Seconds, _ = factValue(v.Facts, "resumeP95")
	ps.FailedResumesPercent, _ = factValue(v.Facts, "resumeFailures")
	ps.Crashes, _ = factValue(v.Facts, "crashes")
	ps.SchedulableWorkers, ps.DesiredWorkers = factValue(v.Facts, "schedulableWorkers")
	return ps
}

func nodeServiceOf(resp SubstrateHealthResponse) substrate.NodeService {
	if resp.Verdict == nil {
		return substrate.NodeService{Verdict: substrate.ServiceUnknown, Reason: noVerdictReason(resp)}
	}
	v := resp.Verdict
	ns := substrate.NodeService{Verdict: v.State, Reason: v.Reason}
	for _, f := range v.Facts {
		if f.ID == "overdueUploads" && f.State == SubstrateFactOK {
			ns.OverdueUploads = f.Value
			for _, p := range f.Parts {
				if ns.Bands == nil {
					ns.Bands = map[string]float64{}
				}
				ns.Bands[p.Label] = p.Value
			}
		}
	}
	ns.OldestUnsavedSeconds, _ = factValue(v.Facts, "oldestUnsaved")
	return ns
}
