// Always-on, in-memory performance instrumentation for k8s-ui internals.
// Records ELK layout duration and structureKey rebuild duration so users can
// include them in bug reports via the host app's diagnostics overlay.
// Cost is one performance.now() pair + a 50-entry ring buffer append per
// topology layout — negligible.

const RING_SIZE = 50

interface Ring {
  samples: number[]
  next: number
  count: number
  last: number
}

function makeRing(): Ring {
  return { samples: new Array(RING_SIZE), next: 0, count: 0, last: 0 }
}

function ringAdd(r: Ring, v: number): void {
  r.samples[r.next] = v
  r.next = (r.next + 1) % RING_SIZE
  if (r.count < RING_SIZE) r.count++
  r.last = v
}

export interface SampleWindow {
  count: number
  last: number
  min: number
  p50: number
  p95: number
  p99: number
  max: number
}

function ringSnapshot(r: Ring): SampleWindow {
  if (r.count === 0) return { count: 0, last: 0, min: 0, p50: 0, p95: 0, p99: 0, max: 0 }
  const buf = r.samples.slice(0, r.count).sort((a, b) => a - b)
  const pick = (p: number) => buf[Math.min(buf.length - 1, Math.floor((buf.length - 1) * p))]
  return {
    count: r.count,
    last: r.last,
    min: buf[0],
    p50: pick(0.5),
    p95: pick(0.95),
    p99: pick(0.99),
    max: buf[buf.length - 1],
  }
}

const layoutMs = makeRing()
const structureKeyUs = makeRing()
const lastLayoutNodeCount = { value: 0 }
const lastLayoutEdgeCount = { value: 0 }
let totalLayouts = 0
let totalLayoutsSkipped = 0
let totalStructureKeyComputes = 0

export interface K8sUIPerfSnapshot {
  totalLayouts: number
  totalLayoutsSkipped: number
  totalStructureKeyComputes: number
  lastLayoutNodeCount: number
  lastLayoutEdgeCount: number
  layoutMs: SampleWindow
  structureKeyUs: SampleWindow
}

export function recordLayoutDuration(ms: number, nodeCount: number, edgeCount: number): void {
  totalLayouts++
  ringAdd(layoutMs, ms)
  lastLayoutNodeCount.value = nodeCount
  lastLayoutEdgeCount.value = edgeCount
}

export function recordLayoutSkipped(): void {
  totalLayoutsSkipped++
}

export function recordStructureKeyDuration(us: number): void {
  totalStructureKeyComputes++
  ringAdd(structureKeyUs, us)
}

export function getK8sUIPerfSnapshot(): K8sUIPerfSnapshot {
  return {
    totalLayouts,
    totalLayoutsSkipped,
    totalStructureKeyComputes,
    lastLayoutNodeCount: lastLayoutNodeCount.value,
    lastLayoutEdgeCount: lastLayoutEdgeCount.value,
    layoutMs: ringSnapshot(layoutMs),
    structureKeyUs: ringSnapshot(structureKeyUs),
  }
}

// Test seam — reset all counters and windows. Not safe to call concurrently
// with the record functions.
export function resetK8sUIPerf(): void {
  layoutMs.samples = new Array(RING_SIZE)
  layoutMs.next = 0; layoutMs.count = 0; layoutMs.last = 0
  structureKeyUs.samples = new Array(RING_SIZE)
  structureKeyUs.next = 0; structureKeyUs.count = 0; structureKeyUs.last = 0
  totalLayouts = 0
  totalLayoutsSkipped = 0
  totalStructureKeyComputes = 0
  lastLayoutNodeCount.value = 0
  lastLayoutEdgeCount.value = 0
}
