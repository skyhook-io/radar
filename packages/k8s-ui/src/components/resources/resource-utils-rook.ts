import { isApiGroup } from './resource-utils-cnpg'
import { healthColors, formatAge, type StatusBadge } from './resource-utils'
import { formatRelativeAgeTime } from '../../utils/format'

export const ROOK_CEPH_GROUP = 'ceph.rook.io'

export const isCephCluster = (data: any): boolean => data?.kind === 'CephCluster' && isApiGroup(data.apiVersion, ROOK_CEPH_GROUP)
export const isCephBlockPool = (data: any): boolean => data?.kind === 'CephBlockPool' && isApiGroup(data.apiVersion, ROOK_CEPH_GROUP)

const badge = (text: string, level: StatusBadge['level']): StatusBadge => ({ text, level, color: healthColors[level] })

function nonEmptyString(v: unknown): string | undefined {
  return typeof v === 'string' && v ? v : undefined
}

function parseTime(v: unknown): number | undefined {
  if (!nonEmptyString(v)) return undefined
  const at = Date.parse(v as string)
  return Number.isNaN(at) ? undefined : at
}

const DEFAULT_HEALTH_CHECK_INTERVAL_MS = 60_000
const MIN_STALE_AFTER_MS = 10 * 60_000
const STALE_INTERVALS = 3
/** How far ahead of this clock lastChecked may be. Mirrors maxClockSkew in pkg/rook. */
const MAX_CLOCK_SKEW_MS = 5 * 60_000
// The units of a Go duration in milliseconds, two-letter ones first.
const GO_DURATION_UNITS: [string, number][] = [['ns', 1e-6], ['us', 1e-3], ['µs', 1e-3], ['μs', 1e-3], ['ms', 1], ['s', 1000], ['m', 60_000], ['h', 3_600_000]]

const isDigit = (c: string | undefined): boolean => c !== undefined && c >= '0' && c <= '9'

/**
 * Milliseconds in a Go duration ("60s", "1m30s", "+.5h"), or undefined where
 * time.ParseDuration fails. Scanned by hand, in one pass.
 */
export function parseGoDuration(text: unknown): number | undefined {
  if (typeof text !== 'string') return undefined
  const sign = text[0] === '-' ? -1 : 1
  let at = text[0] === '-' || text[0] === '+' ? 1 : 0
  if (text.slice(at) === '0') return 0
  if (at === text.length) return undefined
  let total = 0
  while (at < text.length) {
    const start = at
    while (isDigit(text[at])) at++
    if (text[at] === '.') {
      at++
      while (isDigit(text[at])) at++
    }
    const amount = text.slice(start, at)
    if (amount === '' || amount === '.') return undefined
    const unit = GO_DURATION_UNITS.find(([name]) => text.startsWith(name, at))
    if (!unit) return undefined
    at += unit[0].length
    total += Number(amount) * unit[1]
  }
  return sign * total
}

/**
 * How old status.ceph.lastChecked may be before the report is stale: three
 * health-check intervals, never less than ten minutes. The operator's
 * ROOK_CEPH_STATUS_CHECK_INTERVAL can override the spec, out of Radar's view.
 * Mirrors StaleAfter in pkg/rook.
 */
export function getCephStaleAfterMs(data: any): number {
  const interval = parseGoDuration(data?.spec?.healthCheck?.daemonHealth?.status?.interval)
  return Math.max(MIN_STALE_AFTER_MS, STALE_INTERVALS * (interval && interval > 0 ? interval : DEFAULT_HEALTH_CHECK_INTERVAL_MS))
}

export type CephHealthState = 'fresh' | 'stale' | 'disabled' | 'missing'

export interface CephHealthCheck { code: string; severity: string; message: string }

export interface CephHealth {
  /** What Ceph reported (HEALTH_OK, HEALTH_WARN, HEALTH_ERR); empty when nothing was. */
  status: string
  state: CephHealthState
  lastChecked?: string
  lastChanged?: string
  previous?: string
  /** HEALTH_ERR first, then by code. */
  checks: CephHealthCheck[]
}

/** The Ceph health Rook last copied from `ceph status`. Mirrors ClusterHealth in pkg/rook. */
export function getCephHealth(data: any, now = Date.now()): CephHealth {
  const ceph = data?.status?.ceph
  const status = nonEmptyString(ceph?.health) ?? ''
  const checkedAt = parseTime(ceph?.lastChecked)
  const details = ceph?.details && typeof ceph.details === 'object' ? ceph.details : {}
  const checks: CephHealthCheck[] = Object.entries(details).map(([code, detail]: [string, any]) => ({
    code,
    severity: nonEmptyString(detail?.severity) ?? '',
    message: typeof detail?.message === 'string' ? detail.message.trim() : '',
  }))
  checks.sort((a, b) => {
    const errA = a.severity === 'HEALTH_ERR', errB = b.severity === 'HEALTH_ERR'
    if (errA !== errB) return errA ? -1 : 1
    return a.code < b.code ? -1 : a.code > b.code ? 1 : 0
  })
  let state: CephHealthState
  if (data?.spec?.healthCheck?.daemonHealth?.status?.disabled === true) state = 'disabled'
  else if (!status) state = 'missing'
  else if (checkedAt === undefined || now - checkedAt > getCephStaleAfterMs(data) || checkedAt - now > MAX_CLOCK_SKEW_MS) state = 'stale'
  else state = 'fresh'
  return {
    status,
    state,
    lastChecked: checkedAt === undefined ? undefined : ceph.lastChecked,
    lastChanged: parseTime(ceph?.lastChanged) === undefined ? undefined : ceph.lastChanged,
    previous: nonEmptyString(ceph?.previousHealth),
    checks,
  }
}

/** Whether Rook stamped the last check further ahead of this clock than ordinary skew, so the report's age is unknown. */
export function isCephCheckAhead(health: CephHealth, now = Date.now()): boolean {
  const checkedAt = parseTime(health.lastChecked)
  return checkedAt !== undefined && checkedAt - now > MAX_CLOCK_SKEW_MS
}

/**
 * Rook's last reconcile failing: the condition named by status.phase is False
 * and status.message holds the error. Rook keeps retrying. Mirrors
 * ClusterReconcileFailure in pkg/rook.
 */
export function getCephReconcileFailure(data: any): { message: string } | undefined {
  const phase = nonEmptyString(data?.status?.phase)
  if (!phase) return undefined
  const conditions = Array.isArray(data.status.conditions) ? data.status.conditions : []
  const condition = conditions.find((c: any) => c?.type === phase)
  if (condition?.status !== 'False') return undefined
  const message = nonEmptyString(data.status.message) ?? nonEmptyString(condition.message) ?? ''
  return { message: message.trim() }
}

const HEALTH_LEVEL: Record<string, StatusBadge['level']> = { HEALTH_OK: 'healthy', HEALTH_WARN: 'degraded', HEALTH_ERR: 'unhealthy' }

/**
 * A CephCluster's status. Current Ceph health leads, except that a failed
 * reconcile outranks HEALTH_WARN and HEALTH_OK: Ceph can be healthy while Rook
 * can't apply the spec. A stale, disabled or missing report never reads as
 * healthy. Mirrors ClusterStatus in pkg/rook; both run over
 * pkg/rook/testdata/status.json.
 */
export function getCephClusterStatus(data: any, now = Date.now()): StatusBadge {
  if (data?.metadata?.deletionTimestamp) return badge('Terminating', 'neutral')
  const health = getCephHealth(data, now)
  if (health.state === 'fresh' && health.status === 'HEALTH_ERR') return badge('HEALTH_ERR', 'unhealthy')
  if (getCephReconcileFailure(data)) return badge('Reconcile failed', 'degraded')
  if (health.state === 'fresh') return badge(health.status, HEALTH_LEVEL[health.status] ?? 'unknown')
  if (health.state === 'disabled') return badge('Health checks disabled', 'unknown')
  if (health.state === 'stale') return badge('Health stale', 'unknown')
  return badge(nonEmptyString(data?.status?.phase) ?? 'Not reported', 'unknown')
}

/** Why the cluster reads as it does, for the status tooltip. */
export function getCephClusterStatusReason(data: any, now = Date.now()): string | undefined {
  const status = getCephClusterStatus(data, now).text
  const health = getCephHealth(data, now)
  switch (status) {
    case 'HEALTH_ERR':
    case 'HEALTH_WARN':
      return health.checks.map(c => c.message ? `${c.code}: ${c.message}` : c.code).join('\n') || undefined
    case 'Reconcile failed':
      return getCephReconcileFailure(data)?.message || undefined
    case 'Health stale': {
      const when = !health.lastChecked ? 'with no check time'
        : isCephCheckAhead(health, now) ? 'with a check time ahead of this clock'
        : formatRelativeAgeTime(health.lastChecked)
      return `Rook last reported ${health.status} ${when}; the current health is unknown.`
    }
    case 'Health checks disabled':
      return 'spec.healthCheck.daemonHealth.status.disabled stops Rook from refreshing the Ceph health.'
    default:
      return undefined
  }
}

export interface CephCapacity { used: number; total: number; available?: number; lastUpdated?: string }

function byteCount(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : undefined
}

/** Raw capacity from the last `ceph status` that reported it; Rook keeps it between checks that report none. */
export function getCephCapacity(data: any): CephCapacity | undefined {
  const capacity = data?.status?.ceph?.capacity
  const used = byteCount(capacity?.bytesUsed)
  const total = byteCount(capacity?.bytesTotal)
  if (used === undefined || !total) return undefined
  return { used, total, available: byteCount(capacity?.bytesAvailable), lastUpdated: nonEmptyString(capacity?.lastUpdated) }
}

const BINARY_UNITS = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB']

/** Mirrors cephBytes in pkg/ai/context. */
export function formatCephBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  let value = bytes
  let unit = ''
  for (const next of BINARY_UNITS) {
    value /= 1024
    unit = next
    if (value < 1024) break
  }
  return `${value.toFixed(1)} ${unit}`
}

/** OSD count by object store, from status.storage.osd.storeType. */
export function getCephOSDStores(data: any): { total: number; stores: [string, number][] } | undefined {
  const storeType = data?.status?.storage?.osd?.storeType
  if (!storeType || typeof storeType !== 'object') return undefined
  const stores = Object.entries(storeType).filter((e): e is [string, number] => Number.isInteger(e[1]) && (e[1] as number) >= 0)
  if (stores.length === 0) return undefined
  return { total: stores.reduce((sum, [, n]) => sum + n, 0), stores }
}

export function getCephDeviceClasses(data: any): string[] {
  const classes = data?.status?.storage?.deviceClasses
  return Array.isArray(classes) ? classes.map((c: any) => nonEmptyString(c?.name)).filter((n): n is string => !!n) : []
}

/** Messages of True DeletionIsBlocked / PoolDeletionIsBlocked conditions. Mirrors BlockPoolDeletionBlocked in pkg/rook. */
export function getCephBlockPoolDeletionBlocked(data: any): string[] {
  const conditions = Array.isArray(data?.status?.conditions) ? data.status.conditions : []
  return conditions
    .filter((c: any) => c?.status === 'True' && typeof c.type === 'string' && c.type.endsWith('DeletionIsBlocked'))
    .map((c: any) => (typeof c.message === 'string' ? c.message.trim() : ''))
}

const POOL_LEVEL: Record<string, StatusBadge['level']> = { Ready: 'healthy', Failure: 'degraded', Progressing: 'unknown' }

/**
 * Rook's phase for the pool: whether Rook configured it, not whether its
 * placement groups are healthy. Mirrors BlockPoolStatus in pkg/rook.
 */
export function getCephBlockPoolStatus(data: any): StatusBadge {
  if (data?.metadata?.deletionTimestamp) {
    return getCephBlockPoolDeletionBlocked(data).length > 0 ? badge('Deletion blocked', 'degraded') : badge('Terminating', 'neutral')
  }
  const phase = nonEmptyString(data?.status?.phase)
  if (!phase) return badge('Not reported', 'unknown')
  return badge(phase, POOL_LEVEL[phase] ?? 'unknown')
}

export function getCephBlockPoolStatusReason(data: any): string | undefined {
  const status = getCephBlockPoolStatus(data).text
  if (status === 'Deletion blocked') return getCephBlockPoolDeletionBlocked(data).filter(Boolean).join('\n') || undefined
  if (status === 'Failure') return nonEmptyString(data.status?.message)
  return undefined
}

function positiveInteger(v: unknown): number | undefined {
  return Number.isInteger(v) && (v as number) > 0 ? (v as number) : undefined
}

/**
 * Configured data protection. An erasure-coded pool still carries a defaulted
 * replicated block, so erasure coding wins. Mirrors BlockPoolProtection in pkg/rook.
 */
export function getCephBlockPoolProtection(data: any): { kind: 'replicated'; size: number; text: string } | { kind: 'erasureCoded'; data: number; coding: number; text: string } | undefined {
  const ec = data?.spec?.erasureCoded
  const dataChunks = positiveInteger(ec?.dataChunks) ?? 0
  const codingChunks = positiveInteger(ec?.codingChunks) ?? 0
  if (dataChunks > 0 || codingChunks > 0) return { kind: 'erasureCoded', data: dataChunks, coding: codingChunks, text: `Erasure coded, ${dataChunks}+${codingChunks}` }
  const size = positiveInteger(data?.spec?.replicated?.size)
  return size ? { kind: 'replicated', size, text: `Replicated, size ${size}` } : undefined
}

/** spec.failureDomain, or the default Rook resolved and recorded in status.info. */
export function getCephBlockPoolFailureDomain(data: any): string | undefined {
  return nonEmptyString(data?.spec?.failureDomain) ?? nonEmptyString(data?.status?.info?.failureDomain)
}

function healthCheckCodes(data: any): string {
  return getCephHealth(data).checks.map(c => c.code).join(', ')
}

/** Capacity is only as current as the health report: Rook keeps the last one between checks. Mirrors the summary in pkg/ai/context. */
function currentCephCapacity(data: any): CephCapacity | undefined {
  return getCephHealth(data).state === 'fresh' ? getCephCapacity(data) : undefined
}

export function getRookColumnValue(data: any, column: string): string | undefined {
  if (isCephCluster(data)) {
    switch (column) {
      case 'status': return getCephClusterStatus(data).text
      case 'cephPhase': return nonEmptyString(data.status?.phase) ?? ''
      case 'cephMessage': return nonEmptyString(data.status?.message) ?? ''
      case 'cephHealthChecks': {
        const health = getCephHealth(data)
        return health.state === 'fresh' ? healthCheckCodes(data) : ''
      }
      case 'cephCapacity': {
        const capacity = currentCephCapacity(data)
        return capacity ? `${formatCephBytes(capacity.used)} / ${formatCephBytes(capacity.total)}` : ''
      }
      case 'cephVersion': return nonEmptyString(data.status?.version?.version) ?? ''
      case 'cephMons': return positiveInteger(data.spec?.mon?.count)?.toString() ?? ''
      case 'cephExternal': return data.spec?.external?.enable === true ? 'Yes' : 'No'
      case 'cephLastChecked': {
        const at = getCephHealth(data).lastChecked
        return at ? formatAge(at) : ''
      }
      default: return undefined
    }
  }
  if (isCephBlockPool(data)) {
    switch (column) {
      case 'status': return getCephBlockPoolStatus(data).text
      case 'cephPoolProtection': return getCephBlockPoolProtection(data)?.text ?? ''
      case 'cephFailureDomain': return getCephBlockPoolFailureDomain(data) ?? ''
      case 'cephDeviceClass': return nonEmptyString(data.spec?.deviceClass) ?? ''
      case 'cephMirroring': return data.spec?.mirroring?.enabled === true ? (nonEmptyString(data.spec.mirroring.mode) ?? 'Enabled') : 'Off'
      default: return undefined
    }
  }
  return undefined
}

/** Numeric sort keys; -1 sorts unreported values first. */
export function getRookSortValue(data: any, column: string): string | number | undefined {
  const value = getRookColumnValue(data, column)
  if (value === undefined) return undefined
  switch (column) {
    case 'cephCapacity': {
      const capacity = currentCephCapacity(data)
      return capacity ? capacity.used / capacity.total : -1
    }
    case 'cephMons': return positiveInteger(data.spec?.mon?.count) ?? -1
    case 'cephLastChecked': return parseTime(getCephHealth(data).lastChecked) ?? -1
    default: return value
  }
}
