import type { TimeSeries } from '@skyhook-io/k8s-ui/components/charts'
import type { CNPGRuntimeResponse } from '../../api/cnpg'

/** Sessions on one instance at one sample, by pg_stat_activity state. */
export interface InstanceSessions {
  byState: Record<string, number>
  total: number
  waiting?: number
}

// A ring buffer of samples taken while this view is open: the Trends section
// without Prometheus. It says so, and starts empty.
export interface Sample {
  t: number
  /**
   * The Pod the primary's counters came from (name and UID). Counters are per
   * instance, so a rate is never taken across a change of source.
   */
  source?: string
  /**
   * When the exporter generated these values (ms): cnpg_last_update_timestamp
   * when the operator publishes it, else Radar's fetch time.
   */
  metricsAt?: number
  /** metricsAt is Radar's fetch time: the exporter published no generation time. */
  approximate?: boolean
  replayLag: Record<string, number | undefined>
  commits?: number
  rollbacks?: number
  archived?: number
  failed?: number
  blksHit?: number
  blksRead?: number
  walBytes?: number
  deadlocks?: number
  tempBytes?: number
  checkpointsTimed?: number
  checkpointsRequested?: number
  /** Database sizes by name (gauges), every database the exporter reported. */
  dbSizes?: Record<string, number>
  /** Sessions per instance Pod, for every instance whose exporter answered. */
  instances?: Record<string, InstanceSessions>
}

export type CounterKey = 'commits' | 'rollbacks' | 'archived' | 'failed' | 'blksHit' | 'blksRead' | 'deadlocks' | 'tempBytes' | 'checkpointsTimed' | 'checkpointsRequested'

export const SAMPLE_BUFFER_LIMIT = 720

export function sampleFrom(data: CNPGRuntimeResponse): Sample {
  const primary = data.instances.find((i) => i.role === 'primary')
  const replayLag: Record<string, number | undefined> = {}
  for (const r of primary?.status.replication ?? []) replayLag[r.applicationName] = r.replayLag
  const m = primary?.metrics
  const read = m && (m.state === 'ok' || m.state === 'partial') ? m : undefined
  const generation = read?.lastUpdateTimestamp !== undefined ? read.lastUpdateTimestamp * 1000 : undefined
  const fetched = read?.capturedAt ? Date.parse(read.capturedAt) || undefined : undefined
  const instances: Record<string, InstanceSessions> = {}
  for (const inst of data.instances) {
    const im = inst.metrics
    if ((im.state !== 'ok' && im.state !== 'partial') || im.sessionsTotal === undefined) continue
    instances[inst.pod] = { byState: { ...(im.sessionsByState ?? {}) }, total: im.sessionsTotal, waiting: im.waitingBackends }
  }
  return {
    t: Date.parse(data.sampledAt) || Date.now(),
    source: read && primary ? `${primary.pod}/${primary.podUID ?? ''}` : undefined,
    metricsAt: generation ?? fetched,
    approximate: read ? generation === undefined : undefined,
    replayLag,
    commits: read?.xactCommitTotal,
    rollbacks: read?.xactRollbackTotal,
    archived: read?.archiver?.archivedCount,
    failed: read?.archiver?.failedCount,
    blksHit: read?.blksHit,
    blksRead: read?.blksRead,
    walBytes: read?.walBytes,
    deadlocks: read?.deadlocksTotal,
    tempBytes: read?.tempBytesTotal,
    checkpointsTimed: read?.checkpoints?.timed,
    checkpointsRequested: read?.checkpoints?.requested,
    dbSizes: read?.databaseSizes ? Object.fromEntries(read.databaseSizes.map((d) => [d.database, d.bytes])) : undefined,
    instances,
  }
}

/**
 * Walks consecutive exporter generations of the same source. Samples that
 * share a generation are one reading (the exporter and Radar both cache), so
 * only the first counts; a change of source is a gap, never a delta.
 */
function generationPairs(
  samples: Sample[],
  has: (s: Sample) => boolean,
  value: (prev: Sample, s: Sample, seconds: number) => number | null,
): TimeSeries['dataPoints'] {
  const points: TimeSeries['dataPoints'] = []
  let prev: Sample | undefined
  for (const s of samples) {
    if (!has(s) || s.metricsAt === undefined || s.source === undefined) {
      points.push({ timestamp: s.t / 1000, value: null })
      continue
    }
    if (prev && prev.source !== s.source) {
      points.push({ timestamp: s.t / 1000, value: null })
      prev = s
      continue
    }
    if (prev && prev.metricsAt === s.metricsAt) continue
    if (prev) {
      const dt = ((s.metricsAt as number) - (prev.metricsAt as number)) / 1000
      points.push({ timestamp: s.t / 1000, value: dt > 0 ? value(prev, s, dt) : null })
    }
    prev = s
  }
  return points
}

/** A counter as a rate between generations; a decrease is a reset and leaves a gap. `per` scales to per-minute. */
export function rateSeries(samples: Sample[], key: CounterKey, per = 1, label: string = key): TimeSeries {
  return {
    labels: { series: label },
    dataPoints: generationPairs(
      samples,
      (s) => s[key] !== undefined,
      (prev, s, dt) => {
        const dv = (s[key] as number) - (prev[key] as number)
        return dv >= 0 ? (dv / dt) * per : null
      },
    ),
  }
}

/** Cache hit ratio (%) of the blocks read between generations; no reads leaves a gap, not 100 %. */
export function cacheHitSeries(samples: Sample[]): TimeSeries {
  return {
    labels: { series: 'hit ratio' },
    dataPoints: generationPairs(
      samples,
      (s) => s.blksHit !== undefined && s.blksRead !== undefined,
      (prev, s) => {
        const hit = (s.blksHit as number) - (prev.blksHit as number)
        const read = (s.blksRead as number) - (prev.blksRead as number)
        return hit >= 0 && read >= 0 && hit + read > 0 ? (hit / (hit + read)) * 100 : null
      },
    ),
  }
}

/** The latest per-second rate of a counter, or undefined until two generations of one source were seen. */
export function latestRate(samples: Sample[], key: CounterKey): number | undefined {
  const pts = rateSeries(samples, key).dataPoints
  const last = pts[pts.length - 1]
  return last?.value ?? undefined
}

export const SESSION_STATE_GROUPS: { id: string; label: string; states: (state: string) => boolean }[] = [
  { id: 'active', label: 'active', states: (s) => s === 'active' },
  { id: 'idle', label: 'idle', states: (s) => s === 'idle' },
  { id: 'idleInTx', label: 'idle in transaction', states: (s) => s.startsWith('idle in transaction') },
  { id: 'other', label: 'other', states: (s) => s !== 'active' && s !== 'idle' && !s.startsWith('idle in transaction') },
]

/** One instance's sessions by state group; a sample where the instance did not answer is a gap. */
export function sessionStateSeries(samples: Sample[], pod: string): TimeSeries[] {
  return SESSION_STATE_GROUPS.map((g) => ({
    labels: { series: g.label },
    dataPoints: samples.map((s) => {
      const inst = s.instances?.[pod]
      if (!inst) return { timestamp: s.t / 1000, value: null }
      let v = 0
      for (const [state, n] of Object.entries(inst.byState)) if (g.states(state)) v += n
      return { timestamp: s.t / 1000, value: v }
    }),
  }))
}

/** The databases to chart: the five largest in the latest sample unless the viewer picked some. */
export function chartedDatabases(samples: Sample[], picked: string[] | 'all' | null): { all: string[]; shown: string[] } {
  const seen = new Set<string>()
  for (const s of samples) for (const d of Object.keys(s.dbSizes ?? {})) seen.add(d)
  const latest = [...samples].reverse().find((s) => s.dbSizes)?.dbSizes ?? {}
  const all = [...seen].sort((a, b) => (latest[b] ?? -1) - (latest[a] ?? -1) || a.localeCompare(b))
  if (picked === 'all') return { all, shown: all }
  if (picked && picked.length > 0) return { all, shown: all.filter((d) => picked.includes(d)) }
  return { all, shown: all.slice(0, 5) }
}
