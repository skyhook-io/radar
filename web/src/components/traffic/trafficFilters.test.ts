import { describe, it, expect } from 'vitest'
import type { AggregatedFlow, TrafficFlow } from '../../types'
import { matchesStatusRanges, bucketsFromCounts, bucketsFromStatus, isRateBasedSource, keepAvailable, effectiveThreshold, volumeUnit, isExternalKind, isPolicyDropReason, requestRateOf, errorRateOf, formatRate, displayVolume, mergeFlowVolume, dedupeHTTPPairs, coverageLabel, latencyWeightOf, endpointPair, selectionRawPairs, mergeRawPairs, graphEndpoint, graphSize, parseFocus, focusId, touchesFocus, focusNeighborhood, endpointSummaries, searchEndpoints, namespaceSummaries, connectionRows, type GraphFlow } from './trafficFilters'

describe('matchesStatusRanges', () => {
  it('does not filter when nothing is selected', () => {
    expect(matchesStatusRanges(new Set(), [], false)).toBe(true)
  })

  it('matches an event-based flow on its own status bucket', () => {
    const ranges = new Set(['5xx'])
    expect(matchesStatusRanges(ranges, bucketsFromStatus(503), false)).toBe(true)
    expect(matchesStatusRanges(ranges, bucketsFromStatus(200), false)).toBe(false)
  })

  it('matches an aggregate on the buckets it actually reports', () => {
    const counts = { '2xx': 40, '5xx': 3 }
    expect(matchesStatusRanges(new Set(['5xx']), bucketsFromCounts(counts), false)).toBe(true)
    expect(matchesStatusRanges(new Set(['4xx']), bucketsFromCounts(counts), false)).toBe(false)
  })

  it('ignores a bucket present but zero', () => {
    expect(matchesStatusRanges(new Set(['5xx']), bucketsFromCounts({ '5xx': 0 }), false)).toBe(false)
  })

  // The load-bearing case. A rate-based source measures a 5xx rate rather than
  // observing individual responses, so it has no status code and no bucket. The
  // filter used to require one, which hid exactly the failing traffic the user
  // had asked to see.
  it('finds a failing edge that reports an error signal but no status', () => {
    expect(matchesStatusRanges(new Set(['5xx']), [], true)).toBe(true)
    expect(matchesStatusRanges(new Set(['5xx']), bucketsFromStatus(undefined), true)).toBe(true)
    expect(matchesStatusRanges(new Set(['5xx']), bucketsFromCounts(undefined), true)).toBe(true)
  })

  it('does not let an error signal satisfy a non-5xx selection', () => {
    expect(matchesStatusRanges(new Set(['2xx']), [], true)).toBe(false)
    expect(matchesStatusRanges(new Set(['4xx']), [], true)).toBe(false)
  })

  it('still matches a selected bucket when there are no errors', () => {
    expect(matchesStatusRanges(new Set(['2xx', '5xx']), bucketsFromStatus(204), false)).toBe(true)
  })
})

describe('isRateBasedSource', () => {
  it('knows which sources report rates rather than counts', () => {
    expect(isRateBasedSource('beyla')).toBe(true)
    expect(isRateBasedSource('istio')).toBe(true)
    expect(isRateBasedSource('hubble')).toBe(false)
    expect(isRateBasedSource('caretta')).toBe(false)
    expect(isRateBasedSource(undefined)).toBe(false)
    expect(isRateBasedSource('')).toBe(false)
  })
})

describe('keepAvailable', () => {
  it('drops a choice whose control is no longer offered', () => {
    // The button is gone, so the selection must stop filtering — otherwise the map
    // goes blank with nothing left to clear.
    expect(keepAvailable(new Set(['5xx']), [])).toEqual(new Set())
    expect(keepAvailable(new Set(['2xx', '5xx']), ['5xx'])).toEqual(new Set(['5xx']))
  })

  it('leaves an empty selection alone', () => {
    expect(keepAvailable(new Set(), ['5xx'])).toEqual(new Set())
  })

  it('keeps everything still on offer', () => {
    expect(keepAvailable(new Set(['GET', 'POST']), ['GET', 'POST', 'PUT'])).toEqual(new Set(['GET', 'POST']))
  })
})

describe('effectiveThreshold', () => {
  it('keeps a threshold chosen under the unit still in use', () => {
    expect(effectiveThreshold(100, 'connections', 'connections')).toBe(100)
    expect(effectiveThreshold(10, 'rate', 'rate')).toBe(10)
    expect(effectiveThreshold(0, 'rate', 'rate')).toBe(0)
  })

  it('drops one chosen under the other unit', () => {
    // 10000 connections against a rate source would hide every edge.
    expect(effectiveThreshold(10000, 'connections', 'rate')).toBe(0)
    expect(effectiveThreshold(1, 'rate', 'connections')).toBe(0)
  })

  // The load-bearing case: these numbers are steps in BOTH scales, so membership in
  // the active list cannot tell them apart. "100+ connections" is a mild filter;
  // reinterpreted as "100+ req/s" it empties a map whose busiest edge runs at 6/s,
  // while the dropdown still shows a deliberate-looking selection.
  it('drops a value that exists in both scales but was chosen under the other', () => {
    expect(effectiveThreshold(100, 'connections', 'rate')).toBe(0)
    expect(effectiveThreshold(1000, 'connections', 'rate')).toBe(0)
    expect(effectiveThreshold(100, 'rate', 'connections')).toBe(0)
  })
})

describe('volumeUnit', () => {
  it('names the quantity each kind of source measures', () => {
    expect(volumeUnit(true)).toBe('rate')
    expect(volumeUnit(false)).toBe('connections')
    expect(volumeUnit(undefined)).toBe('connections')
  })
})

describe('isExternalKind', () => {
  it('places the world, nodes and unidentified endpoints outside the workloads', () => {
    expect(isExternalKind('External')).toBe(true)
    expect(isExternalKind('Host')).toBe(true)
    expect(isExternalKind('Unknown')).toBe(true)
  })
  it('keeps pods and services inside', () => {
    expect(isExternalKind('Pod')).toBe(false)
    expect(isExternalKind('Service')).toBe(false)
  })
})

describe('isPolicyDropReason', () => {
  it('recognises both of Hubble\'s policy drop codes', () => {
    expect(isPolicyDropReason('POLICY_DENIED', 0)).toBe(true)
    expect(isPolicyDropReason('POLICY_DENY', 0)).toBe(true)
  })
  it('takes the plugin naming a policy as evidence even without a code', () => {
    expect(isPolicyDropReason(undefined, 1)).toBe(true)
  })
  it('does not assume policy for other or missing reasons', () => {
    expect(isPolicyDropReason('STALE_OR_UNROUTABLE_IP', 0)).toBe(false)
    expect(isPolicyDropReason(undefined, 0)).toBe(false)
    expect(isPolicyDropReason('', 0)).toBe(false)
  })
})

describe('edge rates', () => {
  it('reads the unrounded rates, so a trickle of errors is not a 100% error rate', () => {
    const flow = { requestRate: 0.3, requestCount: 1, errorRate: 0.01, errorCount: 1 }
    expect(errorRateOf(flow) / requestRateOf(flow)).toBeCloseTo(0.0333, 3)
  })

  it('falls back to the rounded counts from a server that sends no rates', () => {
    expect(requestRateOf({ requestCount: 7 })).toBe(7)
    expect(errorRateOf({ errorCount: 2 })).toBe(2)
    expect(requestRateOf({})).toBe(0)
  })

  it('shows the unrounded rate for a rate-based edge, and the connection count otherwise', () => {
    expect(displayVolume({ requestRate: 0.3, connections: 1 }, true)).toBe(0.3)
    expect(displayVolume({ connections: 4 }, true)).toBe(4)
    expect(displayVolume({ requestRate: 0.3, connections: 12 }, false)).toBe(12)
  })

  it('formats a rate without rounding a trickle away', () => {
    expect(formatRate(0)).toBe('0')
    expect(formatRate(0.004)).toBe('<0.01')
    expect(formatRate(0.3)).toBe('0.30')
    expect(formatRate(2.46)).toBe('2.5')
    expect(formatRate(12.4)).toBe('12')
    expect(formatRate(1530)).toBe('1.5K')
  })
})

describe('mergeFlowVolume', () => {
  it('sums every figure the graph displays, not only connections and bytes', () => {
    const base = { source: { name: 'a', namespace: '', kind: 'External' }, destination: { name: 'web', namespace: 'shop', kind: 'Pod' }, protocol: 'tcp', port: 80, lastSeen: '' }
    const into = { ...base, flowCount: 1, bytesSent: 10, bytesRecv: 20, connections: 2, requestCount: 2, requestRate: 2, errorRate: 0.5, errorCount: 1 }
    mergeFlowVolume(into, { ...base, flowCount: 1, bytesSent: 5, bytesRecv: 5, connections: 8, requestCount: 8, requestRate: 8 })
    expect(into).toMatchObject({ connections: 10, bytesSent: 15, bytesRecv: 25, flowCount: 2, requestCount: 10, requestRate: 10, errorRate: 0.5, errorCount: 1 })
  })
})

describe('mergeFlowVolume latency', () => {
  const base: AggregatedFlow = { source: { name: 'a', namespace: 'shop', kind: 'Pod' }, destination: { name: 'web', namespace: 'shop', kind: 'Pod' }, protocol: 'tcp', port: 80, lastSeen: '', flowCount: 1, bytesSent: 0, bytesRecv: 0, connections: 1 }

  it('weights average latency by request rate, whatever the order', () => {
    const fast = { ...base, requestRate: 100, avgLatencyMs: 10 }
    const slow = { ...base, requestRate: 1, avgLatencyMs: 1000 }
    const a = { ...fast }
    mergeFlowVolume(a, slow)
    const b = { ...slow }
    mergeFlowVolume(b, fast)
    expect(a.avgLatencyMs).toBeCloseTo(19.8, 1)
    expect(b.avgLatencyMs).toBeCloseTo(19.8, 1)
  })

  it('ignores traffic that had no latency measured, in any merge order', () => {
    const a = { ...base, requestRate: 1, avgLatencyMs: 10 }
    const b = { ...base, requestRate: 100 }
    const c = { ...base, requestRate: 1, avgLatencyMs: 1000 }
    for (const order of [[a, b, c], [c, b, a], [a, c, b], [b, a, c]]) {
      const into = { ...order[0] }
      for (const f of order.slice(1)) mergeFlowVolume(into, f)
      expect(into.avgLatencyMs).toBeCloseTo(505, 6)
    }
  })

  it('keeps the weight through a copy of a merged edge', () => {
    const into = { ...base, requestRate: 1, avgLatencyMs: 1000 }
    mergeFlowVolume(into, { ...base, requestRate: 100 })
    const collapsed = { ...into, source: { name: 'Internet', namespace: '', kind: 'Internet' } }
    mergeFlowVolume(collapsed, { ...base, requestRate: 1, avgLatencyMs: 10 })
    expect(collapsed.avgLatencyMs).toBeCloseTo(505, 6)
  })

  it('averages edges without rates equally, however many are merged', () => {
    const into = { ...base, avgLatencyMs: 10 }
    mergeFlowVolume(into, { ...base, avgLatencyMs: 20 })
    mergeFlowVolume(into, { ...base, avgLatencyMs: 30 })
    expect(into.avgLatencyMs).toBeCloseTo(20, 6)
  })

  it('takes the only latency there is', () => {
    const into = { ...base }
    mergeFlowVolume(into, { ...base, avgLatencyMs: 7 })
    expect(into.avgLatencyMs).toBe(7)
  })
})

describe('dedupeHTTPPairs', () => {
  const ep = (name: string) => ({ name, namespace: 'shop', kind: 'Pod' })
  const rec = (from: string, to: string, l7Type: string, port = 80): TrafficFlow => ({
    source: ep(from), destination: ep(to), protocol: 'tcp', port, l7Protocol: 'HTTP', l7Type,
    httpMethod: 'GET', httpPath: '/orders', bytesSent: 0, bytesRecv: 0, connections: 1, verdict: 'forwarded', lastSeen: '',
  } as TrafficFlow)
  const rows = (fs: TrafficFlow[]) => fs.map(f => `${f.source.name}>${f.destination.name}:${f.port}:${f.l7Type}`)

  describe('from a server whose responses run caller → callee', () => {
    it('pairs a response with its request', () => {
      expect(rows(dedupeHTTPPairs([rec('a', 'b', 'REQUEST'), rec('a', 'b', 'RESPONSE')], true))).toEqual(['a>b:80:RESPONSE'])
    })
    it('keeps an unanswered call the other way on the same route', () => {
      expect(rows(dedupeHTTPPairs([rec('a', 'b', 'RESPONSE'), rec('b', 'a', 'REQUEST')], true))).toEqual(['a>b:80:RESPONSE', 'b>a:80:REQUEST'])
    })
    it('keeps an unanswered call on another port', () => {
      expect(rows(dedupeHTTPPairs([rec('a', 'b', 'REQUEST'), rec('a', 'b', 'RESPONSE'), rec('a', 'b', 'REQUEST', 8080)], true)))
        .toEqual(['a>b:80:RESPONSE', 'a>b:8080:REQUEST'])
    })
  })

  describe('from an older server whose responses run server → client', () => {
    it('pairs the reversed response with its request', () => {
      expect(rows(dedupeHTTPPairs([rec('a', 'b', 'REQUEST'), rec('b', 'a', 'RESPONSE', 41732)], false))).toEqual(['b>a:41732:RESPONSE'])
    })
  })

  it('keeps a request that got no response', () => {
    expect(rows(dedupeHTTPPairs([rec('a', 'b', 'REQUEST')], true))).toEqual(['a>b:80:REQUEST'])
  })
})

describe('merged latency', () => {
  const base: AggregatedFlow = { source: { name: 'a', namespace: 'shop', kind: 'Pod' }, destination: { name: 'web', namespace: 'shop', kind: 'Pod' }, protocol: 'tcp', port: 80, lastSeen: '', flowCount: 1, bytesSent: 0, bytesRecv: 0, connections: 1 }

  it('drops percentiles once two edges with latency are merged', () => {
    const into = { ...base, avgLatencyMs: 10, latencySamples: 90, latencyP50Ms: 9, latencyP95Ms: 30, latencyP99Ms: 40 }
    mergeFlowVolume(into, { ...base, avgLatencyMs: 100, latencySamples: 10, latencyP50Ms: 90, latencyP95Ms: 300, latencyP99Ms: 400 })
    expect(into.latencyP50Ms).toBeUndefined()
    expect(into.latencyP95Ms).toBeUndefined()
    expect(into.avgLatencyMs).toBeCloseTo(19, 6)
  })

  it('keeps the percentiles of the only edge that had latency', () => {
    const into = { ...base }
    mergeFlowVolume(into, { ...base, avgLatencyMs: 10, latencySamples: 5, latencyP95Ms: 30 })
    expect(into.latencyP95Ms).toBe(30)
    mergeFlowVolume(into, { ...base })
    expect(into.latencyP95Ms).toBe(30)
  })

  it('weights a response-level edge by its samples', () => {
    expect(latencyWeightOf({ ...base, avgLatencyMs: 5, latencySamples: 42 })).toBe(42)
    expect(latencyWeightOf({ ...base, avgLatencyMs: 5, requestRate: 3, latencySamples: 42 })).toBe(3)
    expect(latencyWeightOf({ ...base })).toBe(0)
  })
})

describe('coverageLabel', () => {
  it('says how far back the flows reach from when they were collected', () => {
    expect(coverageLabel('2026-09-30T08:37:24Z', '2026-09-30T08:39:04Z')).toBe('last 1m 40s')
    expect(coverageLabel('2026-09-30T08:38:59Z', '2026-09-30T08:39:04Z')).toBe('last 5s')
  })
  it('is empty when the window is fully covered', () => {
    expect(coverageLabel(undefined, '2026-09-30T08:39:04Z')).toBeNull()
  })
})

describe('selectionRawPairs', () => {
  const edge = (src: [string, string, string], dst: [string, string, string]): AggregatedFlow => ({
    source: { namespace: src[0], name: src[1], kind: src[2] },
    destination: { namespace: dst[0], name: dst[1], kind: dst[2] },
    protocol: 'tcp', port: 443, flowCount: 1, bytesSent: 0, bytesRecv: 0, connections: 1, lastSeen: '',
  })
  const a = edge(['shop', 'web-1', 'Pod'], ['', '52.1.1.1', 'External'])
  const b = edge(['shop', 'web-1', 'Pod'], ['', '52.1.1.2', 'External'])
  const c = edge(['shop', 'web-1', 'Pod'], ['shop', 'db-0', 'Pod'])
  // The graph merged a and b into one edge to a renamed external service.
  const merged: GraphFlow = {
    ...a,
    destination: { namespace: '', name: 'HTTPS', kind: 'External' },
    rawPairs: [endpointPair(a), endpointPair(b)],
  }
  const plain: GraphFlow = { ...c, rawPairs: [endpointPair(c)] }

  it('traces a renamed node back to every server edge merged into it', () => {
    const pairs = selectionRawPairs([merged, plain], { type: 'node', nodeId: 'HTTPS' })
    expect(pairs?.map(p => p.destination.name).sort()).toEqual(['52.1.1.1', '52.1.1.2'])
  })

  it('selects an edge by direction', () => {
    expect(selectionRawPairs([merged, plain], { type: 'edge', sourceId: 'shop/web-1', destId: 'shop/db-0' }))
      .toEqual([endpointPair(c)])
    expect(selectionRawPairs([merged, plain], { type: 'edge', sourceId: 'shop/db-0', destId: 'shop/web-1' })).toBeNull()
  })

  it('dedupes server edges reached through several graph edges', () => {
    const pairs = selectionRawPairs([merged, plain, { ...plain }], { type: 'node', nodeId: 'shop/web-1' })
    expect(pairs).toHaveLength(3)
  })

  it('returns null when nothing traces back, so the list falls back to the sample', () => {
    expect(selectionRawPairs([merged], { type: 'node', nodeId: 'addon-group' })).toBeNull()
    expect(selectionRawPairs([merged], null)).toBeNull()
  })

  it('selects an edge on its own port: the graph draws one edge per port', () => {
    const http = { ...c, port: 80 }
    const graph: GraphFlow[] = [{ ...c, rawPairs: [endpointPair(c)] }, { ...http, rawPairs: [endpointPair(http)] }]
    expect(selectionRawPairs(graph, { type: 'edge', sourceId: 'shop/web-1', destId: 'shop/db-0', port: 80 }))
      .toEqual([endpointPair(http)])
    expect(endpointPair(http).port).toBe(80)
  })

  it('keeps a merged edge selected whichever of its ports it happens to carry', () => {
    const http = { ...c, port: 80 }
    // Merged from :443 and :80; the merged edge kept :443 this refresh.
    const mergedEdge: GraphFlow = { ...c, rawPairs: [endpointPair(c), endpointPair(http)] }
    expect(selectionRawPairs([mergedEdge], { type: 'edge', sourceId: 'shop/web-1', destId: 'shop/db-0', port: 80 }))
      .toHaveLength(2)
  })

  it('resolves the addon group, drawn as one node, through its virtual endpoints', () => {
    const virtual: GraphFlow = {
      ...a,
      source: { namespace: '', name: 'addon-internet', kind: 'AddonInternet' },
      destination: { namespace: '', name: 'addon-group-target', kind: 'AddonGroupTarget' },
      port: 0,
      rawPairs: [endpointPair(a)],
    }
    expect(selectionRawPairs([virtual], { type: 'edge', sourceId: 'addon-internet', destId: 'addon-group', port: 0 }))
      .toEqual([endpointPair(a)])
    expect(selectionRawPairs([virtual], { type: 'node', nodeId: 'addon-group' })).toEqual([endpointPair(a)])
  })

  it('keeps an edge apart from its unknown-direction twin', () => {
    const twin = { ...c, directionUnknown: true }
    const graph: GraphFlow[] = [{ ...c, rawPairs: [endpointPair(c)] }, { ...twin, rawPairs: [endpointPair(twin)] }]
    expect(selectionRawPairs(graph, { type: 'edge', sourceId: 'shop/web-1', destId: 'shop/db-0', port: 443 }))
      .toEqual([endpointPair(c)])
    expect(selectionRawPairs(graph, { type: 'edge', sourceId: 'shop/web-1', destId: 'shop/db-0', port: 443, directionUnknown: true }))
      .toEqual([endpointPair(twin)])
  })

  it('returns the same order however the server ordered its aggregation', () => {
    const sel = { type: 'node' as const, nodeId: 'shop/web-1' }
    expect(selectionRawPairs([plain, merged], sel)).toEqual(selectionRawPairs([merged, plain], sel))
  })

  it('selects the traffic of every addon inside the group while addons are grouped', () => {
    const dns = edge(['kube-system', 'coredns', 'Pod'], ['shop', 'web-1', 'Pod'])
    const graph: GraphFlow[] = [{ ...dns, rawPairs: [endpointPair(dns)] }, plain]
    const inGroup = (e: { name: string }) => e.name === 'coredns'
    expect(selectionRawPairs(graph, { type: 'node', nodeId: 'addon-group' }, inGroup)).toEqual([endpointPair(dns)])
    expect(selectionRawPairs(graph, { type: 'node', nodeId: 'addon-group' })).toBeNull()
  })

  it('merges pairs in place', () => {
    const into: GraphFlow = { ...a, rawPairs: [endpointPair(a)] }
    const owned = into.rawPairs
    mergeRawPairs(into, { ...b, rawPairs: [endpointPair(b)] })
    expect(into.rawPairs).toBe(owned)
    expect(into.rawPairs).toHaveLength(2)
  })

  it('omits an empty namespace so the server reads it as cluster-level', () => {
    expect(endpointPair(a).destination).toEqual({ namespace: undefined, name: '52.1.1.1', kind: 'External' })
  })
})

describe('graphEndpoint', () => {
  it('names a pod with a known workload by the workload, like the server aggregation', () => {
    expect(graphEndpoint({ namespace: 'shop', name: 'web-7d9f-x2k4q', kind: 'Pod', workload: 'web', workloadKind: 'Deployment' }))
      .toEqual({ namespace: 'shop', name: 'web' })
  })
  it('leaves everything else as it is', () => {
    const bare = { namespace: 'shop', name: 'debug', kind: 'Pod' }
    expect(graphEndpoint(bare)).toBe(bare)
    const ext = { namespace: '', name: 'world', kind: 'External', workload: 'x' }
    expect(graphEndpoint(ext)).toBe(ext)
  })
})

describe('graphSize', () => {
  it('counts distinct nodes and weighs edges double', () => {
    const f = (s: string, d: string): AggregatedFlow => ({
      source: { namespace: 'a', name: s, kind: 'Workload' },
      destination: { namespace: 'a', name: d, kind: 'Workload' },
      protocol: 'tcp', port: 80, flowCount: 1, bytesSent: 0, bytesRecv: 0, connections: 1, lastSeen: '',
    })
    expect(graphSize([f('web', 'db'), f('web', 'cache'), f('api', 'db')])).toEqual({ nodes: 4, edges: 3, score: 10 })
  })
})

function edge(src: string, dst: string, extra: Partial<AggregatedFlow> = {}): AggregatedFlow {
  const ep = (id: string) => {
    const [namespace, name] = id.includes('/') ? id.split('/') : ['', id]
    return { namespace, name, kind: namespace ? 'Workload' : 'External' }
  }
  return {
    source: ep(src), destination: ep(dst), protocol: 'tcp', port: 80,
    flowCount: 1, bytesSent: 0, bytesRecv: 0, connections: 1, lastSeen: '', ...extra,
  }
}

describe('focus', () => {
  it('round-trips through the URL value', () => {
    expect(parseFocus('shop/checkout')).toEqual({ namespace: 'shop', name: 'checkout' })
    expect(focusId(parseFocus('shop/checkout')!)).toBe('shop/checkout')
    // External endpoints have no namespace.
    expect(parseFocus('api.stripe.com')).toEqual({ name: 'api.stripe.com' })
    expect(parseFocus('shop/')).toBeNull()
    expect(parseFocus(null)).toBeNull()
  })

  it('keeps the edges at either end of the focus and nothing else', () => {
    const flows = [edge('shop/web', 'shop/checkout'), edge('shop/checkout', 'pay/api'), edge('shop/web', 'shop/cart')]
    const n = focusNeighborhood(flows, { namespace: 'shop', name: 'checkout' })
    expect(n).toHaveLength(2)
    expect(n).not.toContain(flows[2])
  })

  it('matches by namespace too: a same-named workload elsewhere is not the focus', () => {
    const flows = [edge('other/checkout', 'shop/web')]
    expect(focusNeighborhood(flows, { namespace: 'shop', name: 'checkout' })).toHaveLength(0)
  })

  // Ungrouped, the graph draws pods. A workload focus is then its pods.
  it('matches a workload\'s pods when pods are not grouped', () => {
    const pod = { namespace: 'shop', name: 'checkout-7d9-abc', kind: 'Pod', workload: 'checkout' }
    expect(touchesFocus(pod, { namespace: 'shop', name: 'checkout' })).toBe(true)
    expect(touchesFocus({ ...pod, kind: 'Service' }, { namespace: 'shop', name: 'checkout' })).toBe(false)
  })
})

describe('endpoint search', () => {
  const flows = [
    edge('shop/checkout', 'shop/db', { connections: 5 }),
    edge('shop/cart', 'shop/checkout', { connections: 50 }),
    edge('shop/checkout-worker', 'shop/db', { connections: 1, verdictCounts: { dropped: 2 } }),
  ]
  const summaries = endpointSummaries(flows)

  it('sums an endpoint\'s traffic across its edges, once per edge', () => {
    const checkout = summaries.find(s => s.id === 'shop/checkout')!
    expect(checkout.volume).toBe(55)
    expect(endpointSummaries([edge('shop/a', 'shop/a', { connections: 3 })])[0].volume).toBe(3)
  })

  it('puts an exact name first, then prefixes, problems before volume', () => {
    expect(searchEndpoints(summaries, 'checkout').map(s => s.name)).toEqual(['checkout', 'checkout-worker'])
    const all = searchEndpoints(summaries, 'c').map(s => s.name)
    expect(all.indexOf('checkout-worker')).toBeLessThan(all.indexOf('cart'))
  })

  it('finds nothing for a name with no traffic', () => {
    expect(searchEndpoints(summaries, 'payments')).toEqual([])
  })
})

describe('namespaceSummaries', () => {
  // Narrowing to a namespace keeps edges with either end in it, as the server
  // does, so a cross-namespace edge belongs to both.
  it('counts a cross-namespace edge toward both namespaces', () => {
    const s = namespaceSummaries([edge('shop/web', 'pay/api'), edge('shop/web', 'shop/db')])
    const shop = s.find(n => n.name === 'shop')!
    const pay = s.find(n => n.name === 'pay')!
    expect(shop.connections).toBe(2)
    expect(pay.connections).toBe(1)
    expect(pay.endpoints).toBe(2)
  })

  it('ranks the namespace with failures first', () => {
    const s = namespaceSummaries([
      edge('big/a', 'big/b', { connections: 1000 }),
      edge('small/a', 'small/b', { connections: 1, httpStatusCounts: { '5xx': 1 } }),
    ])
    expect(s.map(n => n.name)).toEqual(['small', 'big'])
  })

  it('skips endpoints without a namespace', () => {
    expect(namespaceSummaries([edge('shop/web', 'api.stripe.com')]).map(n => n.name)).toEqual(['shop'])
  })
})

describe('connectionRows', () => {
  it('lists failing edges first, then by volume', () => {
    const quiet = edge('a/x', 'a/y', { connections: 1 })
    const busy = edge('a/x', 'a/z', { connections: 100 })
    const dropping = edge('a/x', 'a/w', { connections: 2, verdictCounts: { dropped: 1 } })
    const erroring = edge('a/x', 'a/v', { connections: 2, errorRate: 0.5 })
    expect(connectionRows([quiet, busy, erroring, dropping])).toEqual([dropping, erroring, busy, quiet])
  })
})

describe('mergeFlowVolume problem counts', () => {
  it('sums status and verdict counts without touching the merged edges', () => {
    const a = edge('a/x', 'ext', { httpStatusCounts: { '5xx': 1 }, verdictCounts: { dropped: 2 } })
    const b = edge('a/y', 'ext', { httpStatusCounts: { '5xx': 3, '2xx': 4 }, verdictCounts: { forwarded: 5 } })
    const merged = { ...a }
    mergeFlowVolume(merged, b)
    expect(merged.httpStatusCounts).toEqual({ '5xx': 4, '2xx': 4 })
    expect(merged.verdictCounts).toEqual({ dropped: 2, forwarded: 5 })
    expect(a.httpStatusCounts).toEqual({ '5xx': 1 })
  })
})
