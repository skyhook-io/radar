import type { HealthLevel } from '../resources/resource-utils'

/** The Deployment a Pooler runs, as the host read it. */
export interface CNPGPoolerDeploymentLive {
  name: string
  state: 'ok' | 'missing' | 'unreadable' | 'foreign'
  replicas?: number
  readyReplicas?: number
  updatedReplicas?: number
  availableReplicas?: number
}

export interface CNPGPoolerServiceLive {
  name: string
  state: 'ok' | 'missing' | 'unreadable' | 'foreign'
  type?: string
  port?: number
}

export interface CNPGPoolerPoolSample {
  database: string
  user: string
  clActive?: number
  clWaiting?: number
  svActive?: number
  svIdle?: number
  svUsed?: number
  maxwaitSeconds?: number
  poolMode?: string
}

/** Live PgBouncer exporter reads, one per pooler Pod. */
export interface CNPGPoolerPressureLive {
  state: 'loading' | 'denied' | 'error' | 'ok'
  reason?: string
  pods: { pod: string; state: string; error?: string; pools?: CNPGPoolerPoolSample[] }[]
}

/** Each PgBouncer's own SHOW STATE. */
export interface CNPGPoolerObservedLive {
  state: 'loading' | 'denied' | 'error' | 'ok'
  grant?: string
  reason?: string
  pods: { pod: string; state: string; paused?: boolean; error?: string }[]
}

/**
 * What a host that reads the cluster adds to the Pooler summary. Each part is
 * optional: a part the host does not provide keeps the resource-only wording.
 */
export interface CNPGPoolerLive {
  deployment?: CNPGPoolerDeploymentLive
  service?: CNPGPoolerServiceLive
  pressure?: CNPGPoolerPressureLive
  observed?: CNPGPoolerObservedLive
}

export interface CNPGPoolerReadiness {
  text: string
  level: HealthLevel
  detail?: string
}

/** Readiness from the Pooler's Deployment; the Pooler's status only counts scheduled Pods. */
export function poolerReadiness(d: CNPGPoolerDeploymentLive | undefined): CNPGPoolerReadiness {
  if (!d) return { text: 'Unknown', level: 'unknown', detail: 'Readiness is on the Pooler’s Deployment, which was not read' }
  switch (d.state) {
    case 'missing':
      return { text: 'No Deployment', level: 'unhealthy', detail: `Deployment ${d.name} does not exist` }
    case 'unreadable':
      return { text: 'Unknown', level: 'unknown', detail: `No access to Deployment ${d.name}` }
    case 'foreign':
      return { text: 'Unknown', level: 'unknown', detail: `Deployment ${d.name} is not controlled by this Pooler` }
  }
  const want = d.replicas ?? 0
  const ready = d.readyReplicas ?? 0
  const detail = `from Deployment ${d.name}`
  if (want === 0) return { text: 'Scaled to zero', level: 'neutral', detail }
  if (ready === 0) return { text: 'Not ready', level: 'unhealthy', detail }
  if (ready < want) return { text: `${ready}/${want} ready`, level: 'degraded', detail }
  return { text: `${ready}/${want} ready`, level: 'healthy', detail }
}

export interface CNPGPoolerPoolRow {
  database: string
  user: string
  poolModes: string[]
  clActive?: number
  clWaiting?: number
  svActive?: number
  svIdle?: number
  svUsed?: number
  maxwaitSeconds?: number
  /** Pods whose exporter reported this pool. */
  pods: number
}

/**
 * One row per database/user pool, summed over the Pods that reported it
 * (max wait is the maximum). A field no Pod reported stays undefined.
 */
export function aggregatePoolerPools(pods: CNPGPoolerPressureLive['pods']): CNPGPoolerPoolRow[] {
  const rows = new Map<string, CNPGPoolerPoolRow>()
  const add = (a: number | undefined, b: number | undefined) => (b === undefined ? a : (a ?? 0) + b)
  for (const p of pods) {
    for (const pool of p.pools ?? []) {
      const key = `${pool.database}\u0000${pool.user}`
      const row = rows.get(key) ?? { database: pool.database, user: pool.user, poolModes: [], pods: 0 }
      row.pods++
      row.clActive = add(row.clActive, pool.clActive)
      row.clWaiting = add(row.clWaiting, pool.clWaiting)
      row.svActive = add(row.svActive, pool.svActive)
      row.svIdle = add(row.svIdle, pool.svIdle)
      row.svUsed = add(row.svUsed, pool.svUsed)
      if (pool.maxwaitSeconds !== undefined) row.maxwaitSeconds = Math.max(row.maxwaitSeconds ?? 0, pool.maxwaitSeconds)
      if (pool.poolMode && !row.poolModes.includes(pool.poolMode)) row.poolModes.push(pool.poolMode)
      rows.set(key, row)
    }
  }
  return [...rows.values()].sort((a, b) => a.database.localeCompare(b.database) || a.user.localeCompare(b.user))
}

export interface CNPGPoolerPodRow {
  pod: string
  /** ok | partial | the read's failure state. */
  state: string
  error?: string
  clActive?: number
  clWaiting?: number
  svActive?: number
  maxwaitSeconds?: number
}

/**
 * Each PgBouncer Pod's own totals across its pools, so one saturated Pod is
 * not hidden in the sum. A Pod that did not report keeps its state and no
 * numbers; a field none of its pools reported stays undefined.
 */
export function poolerPodPressure(pods: CNPGPoolerPressureLive['pods']): CNPGPoolerPodRow[] {
  const add = (a: number | undefined, b: number | undefined) => (b === undefined ? a : (a ?? 0) + b)
  return pods
    .map((p) => {
      const row: CNPGPoolerPodRow = { pod: p.pod, state: p.state, error: p.error }
      for (const pool of p.pools ?? []) {
        row.clActive = add(row.clActive, pool.clActive)
        row.clWaiting = add(row.clWaiting, pool.clWaiting)
        row.svActive = add(row.svActive, pool.svActive)
        if (pool.maxwaitSeconds !== undefined) row.maxwaitSeconds = Math.max(row.maxwaitSeconds ?? 0, pool.maxwaitSeconds)
      }
      return row
    })
    .sort((a, b) => a.pod.localeCompare(b.pod))
}

/**
 * PgBouncer settings worth showing, with the value PgBouncer uses when the
 * Pooler leaves one unset. Defaults are named only where they were read from
 * PgBouncer itself (SHOW CONFIG on PgBouncer 1.24); CloudNativePG writes only
 * the parameters the Pooler sets.
 */
export const POOLER_LIMIT_PARAMETERS: { key: string; label: string; pgbouncerDefault?: string }[] = [
  { key: 'default_pool_size', label: 'Pool size (per database/user)', pgbouncerDefault: '20' },
  { key: 'max_client_conn', label: 'Max client connections', pgbouncerDefault: '100' },
  { key: 'max_db_connections', label: 'Max connections per database', pgbouncerDefault: '0 (unlimited)' },
  { key: 'max_user_connections', label: 'Max connections per user', pgbouncerDefault: '0 (unlimited)' },
  { key: 'reserve_pool_size', label: 'Reserve pool', pgbouncerDefault: '0' },
  { key: 'min_pool_size', label: 'Min pool size', pgbouncerDefault: '0' },
]

/** The Cluster Service PgBouncer forwards to, by the Pooler's type. */
export function poolerBackendService(cluster: string | undefined, type: string | undefined): string | undefined {
  if (!cluster || !type) return undefined
  if (type === 'rw' || type === 'ro' || type === 'r') return `${cluster}-${type}`
  return undefined
}

/** Observed pause across PgBouncers: every one, none, some, or unknown. */
export function observedPause(o: CNPGPoolerObservedLive | undefined): { text: string; level: HealthLevel } | null {
  if (!o) return null
  if (o.state === 'loading') return { text: 'Reading…', level: 'unknown' }
  if (o.state === 'denied') return { text: `Not observable: needs ${o.grant ?? 'create pods/exec'}`, level: 'unknown' }
  if (o.state === 'error') return { text: `Not observable: ${o.reason ?? 'read failed'}`, level: 'unknown' }
  if (o.pods.length === 0) return { text: 'No PgBouncer Pods', level: 'unknown' }
  const read = o.pods.filter((p) => p.state === 'ok' && p.paused !== undefined)
  const paused = read.filter((p) => p.paused).length
  const unread = o.pods.length - read.length
  const tail = unread > 0 ? ` · ${unread} not read` : ''
  if (read.length === 0) return { text: `Not observable${tail}`, level: 'unknown' }
  if (paused === 0) return { text: `Serving (not paused) on ${read.length} of ${o.pods.length} PgBouncers${tail}`, level: unread ? 'unknown' : 'healthy' }
  if (paused === read.length) return { text: `Paused on ${paused} of ${o.pods.length} PgBouncers${tail}`, level: 'degraded' }
  return { text: `Paused on ${paused} of ${o.pods.length} PgBouncers${tail}`, level: 'alert' }
}
