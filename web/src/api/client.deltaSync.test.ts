import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { runDeltaSyncFetch, type ChangesDeltaMeta } from './client'
import type { TimelineEvent } from '@skyhook-io/k8s-ui'

// Orchestration test for the useChanges delta contract. The pure helpers
// (deltaFetchCursor / mergeDeltaEvents / maxEventSeq) are covered in
// client.delta.test.ts; here we drive the full runDeltaSyncFetch loop through
// the real fetch boundary so the header parsing, cursor priming, epoch-resync,
// and merge all participate.

const BASE_TS = 1_700_000_000_000
const mk = (id: string, seq: number, tsOffsetMs: number, extra: Partial<TimelineEvent> = {}): TimelineEvent => ({
  id,
  seq,
  timestamp: new Date(BASE_TS + tsOffsetMs).toISOString(),
  source: 'informer',
  kind: 'Pod',
  namespace: 'default',
  name: id,
  eventType: 'update',
  ...extra,
})

// A page as the server returns it: an events body plus the two frontier headers
// that fetchChangesPage reads to derive epoch + maxSeq.
interface Page {
  events: TimelineEvent[]
  epoch: string
  maxSeq: number
}
function pageResponse(page: Page): Response {
  return new Response(JSON.stringify(page.events), {
    status: 200,
    headers: {
      'X-Radar-Timeline-Epoch': page.epoch,
      'X-Radar-Timeline-Max-Seq': String(page.maxSeq),
    },
  })
}

// The `since_seq` a fetched URL carries, or null for a full fetch.
function sinceSeqOf(url: string): number | null {
  const m = url.match(/[?&]since_seq=(\d+)/)
  return m ? Number(m[1]) : null
}

let fetchedUrls: string[]
let responder: (url: string) => Page

beforeEach(() => {
  fetchedUrls = []
  vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
    const url = String(input)
    fetchedUrls.push(url)
    return Promise.resolve(pageResponse(responder(url)))
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const PATH = '/changes?limit=200&filter=all'
const QUERY_STRING = 'limit=200&filter=all'
const LIMIT = 200
const META_KEY = 'changes-key'

// A single query's delta loop: fresh meta store per test, cached page threaded
// forward across polls, an explicit `now` so the anti-entropy full-resync timer
// stays out of the way unless a test wants it.
function run(metaStore: Map<string, ChangesDeltaMeta>, cached: TimelineEvent[] | undefined, now: number) {
  return runDeltaSyncFetch({ path: PATH, queryString: QUERY_STRING, limit: LIMIT, metaKey: META_KEY, cached, metaStore, now })
}

const lastUrl = () => fetchedUrls[fetchedUrls.length - 1]

describe('runDeltaSyncFetch (useChanges delta orchestration)', () => {
  it('first fetch (no cursor) is a full fetch; the cursor is primed from the max-seq header', async () => {
    const metaStore = new Map<string, ChangesDeltaMeta>()
    // Header frontier 8 exceeds the highest visible event seq (5): rows dropped
    // by the server's RBAC filter still advance the cursor.
    responder = () => ({ events: [mk('a', 5, 2000), mk('b', 3, 1000)], epoch: 'e1', maxSeq: 8 })

    const result = await run(metaStore, undefined, 1_000)

    expect(fetchedUrls).toHaveLength(1)
    expect(sinceSeqOf(fetchedUrls[0])).toBeNull()
    expect(result.map((e) => e.id)).toEqual(['a', 'b'])

    const meta = metaStore.get(META_KEY)!
    expect(meta.epoch).toBe('e1')
    expect(meta.highWaterSeq).toBe(8)
    expect(meta.lastFullMs).toBe(1_000)
  })

  it('a subsequent poll sends since_seq=<cursor>, merges delta rows by id, and advances the cursor', async () => {
    const metaStore = new Map<string, ChangesDeltaMeta>()
    responder = () => ({ events: [mk('a', 5, 2000), mk('b', 3, 1000)], epoch: 'e1', maxSeq: 5 })
    const first = await run(metaStore, undefined, 1_000)

    // A new arrival (c) plus a re-arrival of 'a' under a higher seq (count bump).
    responder = () => ({ events: [mk('c', 7, 3000), mk('a', 6, 2000, { count: 9 })], epoch: 'e1', maxSeq: 7 })
    const second = await run(metaStore, first, 2_000)

    expect(sinceSeqOf(lastUrl())).toBe(5)
    expect(second.map((e) => e.id)).toEqual(['c', 'a', 'b'])
    expect(second.find((e) => e.id === 'a')!.count).toBe(9)
    expect(metaStore.get(META_KEY)!.highWaterSeq).toBe(7)
  })

  it('an empty delta carrying a max-seq header still advances the cursor', async () => {
    const metaStore = new Map<string, ChangesDeltaMeta>()
    responder = () => ({ events: [mk('a', 5, 2000)], epoch: 'e1', maxSeq: 5 })
    const first = await run(metaStore, undefined, 1_000)

    // The entire page past seq 5 was RBAC-filtered to nothing, but the server
    // reports the frontier it scanned to.
    responder = () => ({ events: [], epoch: 'e1', maxSeq: 12 })
    const second = await run(metaStore, first, 2_000)
    expect(metaStore.get(META_KEY)!.highWaterSeq).toBe(12)

    // The next poll must ride the advanced cursor, not re-request from 5.
    responder = () => ({ events: [], epoch: 'e1', maxSeq: 12 })
    await run(metaStore, second, 3_000)
    expect(sinceSeqOf(lastUrl())).toBe(12)
  })

  it('an empty delta returns the cached array reference (no needless re-render)', async () => {
    const metaStore = new Map<string, ChangesDeltaMeta>()
    responder = () => ({ events: [mk('a', 5, 0)], epoch: 'e1', maxSeq: 5 })
    const first = await run(metaStore, undefined, 1_000)

    responder = () => ({ events: [], epoch: 'e1', maxSeq: 5 })
    const second = await run(metaStore, first, 2_000)
    expect(second).toBe(first)
  })

  it('an epoch change between polls forces a full resync: cursor reset, list replaced', async () => {
    const metaStore = new Map<string, ChangesDeltaMeta>()
    responder = () => ({ events: [mk('old', 9, 0)], epoch: 'e1', maxSeq: 9 })
    const first = await run(metaStore, undefined, 1_000)

    // The store restarted — seq numbering reset low, so a since_seq=9 delta comes
    // back EMPTY under a NEW epoch. Trusting that empty delta as "nothing new"
    // would strand the caller on the previous store's rows; the epoch mismatch
    // must instead trigger a full resync.
    const restarted = [mk('fresh', 2, 500)]
    responder = (url) =>
      sinceSeqOf(url) != null
        ? { events: [], epoch: 'e2', maxSeq: 2 }
        : { events: restarted, epoch: 'e2', maxSeq: 2 }
    const second = await run(metaStore, first, 2_000)

    expect(second).not.toBe(first)
    expect(second.map((e) => e.id)).toEqual(['fresh'])
    // Two fetches: the epoch-mismatched delta probe, then the full resync.
    expect(sinceSeqOf(fetchedUrls[fetchedUrls.length - 2])).toBe(9)
    expect(sinceSeqOf(lastUrl())).toBeNull()

    const meta = metaStore.get(META_KEY)!
    expect(meta.epoch).toBe('e2')
    expect(meta.highWaterSeq).toBe(2)
    expect(meta.lastFullMs).toBe(2_000)
  })

  it('equivalence: full → delta → delta yields the same set as a fresh full fetch of the final state', async () => {
    // A minimal append/replace server keyed by id with monotonic seq. Each fetch
    // serializes a snapshot, so pages the loop keeps are decoupled from later
    // mutations of the log.
    const epoch = 'e1'
    const log: TimelineEvent[] = []
    let nextSeq = 1
    const upsert = (id: string, tsOffsetMs: number, content: number) => {
      const seq = nextSeq++
      const existing = log.find((e) => e.id === id)
      if (existing) {
        existing.seq = seq
        existing.count = content
      } else {
        log.push(mk(id, seq, tsOffsetMs, { count: content }))
      }
    }
    const newestFirst = (rows: TimelineEvent[]) =>
      [...rows].sort((a, b) => {
        const byTime = new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime()
        return byTime !== 0 ? byTime : (b.seq ?? 0) - (a.seq ?? 0)
      })
    responder = (url) => {
      const since = sinceSeqOf(url)
      const rows = since == null ? log : log.filter((e) => (e.seq ?? 0) > since)
      const maxSeq = log.reduce((m, e) => Math.max(m, e.seq ?? 0), 0)
      return { events: newestFirst(rows), epoch, maxSeq }
    }

    const metaStore = new Map<string, ChangesDeltaMeta>()
    upsert('a', 1000, 1)
    upsert('b', 2000, 1)
    upsert('c', 3000, 1)
    const l1 = await run(metaStore, undefined, 1_000)

    upsert('d', 4000, 1)
    upsert('a', 1000, 2) // a count bump re-arrives under the same id
    const l2 = await run(metaStore, l1, 2_000)

    upsert('e', 5000, 1)
    upsert('b', 2000, 2)
    const l3 = await run(metaStore, l2, 3_000)

    // What a fresh client would get from a single full fetch of the final state.
    const fresh = newestFirst(log)
    const asSet = (rows: TimelineEvent[]) => rows.map((e) => `${e.id}:${e.count}`).sort()
    expect(asSet(l3)).toEqual(asSet(fresh))
    // No dropped or duplicated ids after two incremental merges.
    expect(l3).toHaveLength(fresh.length)
  })
})
