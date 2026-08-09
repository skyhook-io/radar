import type { ProbeResult } from './types'

// collapseSkipRows folds identical (layer, path, reason, command) skip rows
// into a single row labelled with the collapsed count. Real probe outcomes
// (success, failure, partial latency rows) pass through unchanged - only
// "didn't run, here's why" rows get deduped. Driving case: a Pods hop
// with three replicas and one non-HTTP port produces three skip rows
// with identical reasons; the operator only needs to read it once. The
// command is part of the key so rows whose fill-the-gap commands differ
// (e.g. a per-pod port-forward) stay separate rather than collapse and
// surface the wrong pod's command.
export function collapseSkipRows(rows: ProbeResult[]): ProbeResult[] {
  const out: ProbeResult[] = []
  const seenIdx = new Map<string, number>()
  const counts = new Map<string, number>()
  for (const r of rows) {
    if (!r.skipped || !r.reason) {
      out.push(r)
      continue
    }
    const key = [r.layer, r.path ?? '', r.reason, r.command ?? ''].join('|')
    if (!seenIdx.has(key)) {
      seenIdx.set(key, out.length)
      counts.set(key, 1)
      out.push(r)
    } else {
      counts.set(key, (counts.get(key) ?? 1) + 1)
    }
  }
  return out.map((r) => {
    if (!r.skipped) return r
    const key = [r.layer, r.path ?? '', r.reason, r.command ?? ''].join('|')
    const n = counts.get(key) ?? 1
    if (n <= 1) return r
    return { ...r, target: `${n} probes skipped` }
  })
}
