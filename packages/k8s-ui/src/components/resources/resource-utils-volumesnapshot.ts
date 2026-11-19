import { isApiGroup } from './resource-utils-cnpg'
import { healthColors, formatAge, type StatusBadge } from './resource-utils'
import { formatBytes, parseQuantity } from '../../utils/format'

export const VOLUME_SNAPSHOT_GROUP = 'snapshot.storage.k8s.io'
const DEFAULT_CLASS_ANNOTATION = 'snapshot.storage.kubernetes.io/is-default-class'

export function isVolumeSnapshot(data: any): boolean {
  return data?.kind === 'VolumeSnapshot' && isApiGroup(data.apiVersion, VOLUME_SNAPSHOT_GROUP)
}

export function isVolumeSnapshotContent(data: any): boolean {
  return data?.kind === 'VolumeSnapshotContent' && isApiGroup(data.apiVersion, VOLUME_SNAPSHOT_GROUP)
}

export function isVolumeSnapshotClass(data: any): boolean {
  return data?.kind === 'VolumeSnapshotClass' && isApiGroup(data.apiVersion, VOLUME_SNAPSHOT_GROUP)
}

const badge = (text: string, level: StatusBadge['level']): StatusBadge => ({ text, level, color: healthColors[level] })

/**
 * Readiness of a VolumeSnapshot or VolumeSnapshotContent as its controller
 * reports it. The snapshot controller retries after an error and clears
 * status.error once the snapshot is ready, so an error is the current failure
 * and never outranks readyToUse. A missing readyToUse is unknown, not false.
 * Ready means the CSI driver can restore from the snapshot; it does not prove
 * application consistency or that a restore was ever tested.
 * Mirrors volumeSnapshotState in pkg/ai/context.
 */
export function getVolumeSnapshotStatus(data: any): StatusBadge {
  if (data?.metadata?.deletionTimestamp) return badge('Terminating', 'neutral')
  const status = data?.status
  const ready = status?.readyToUse
  if (ready === true) return badge('Ready', 'healthy')
  if (status?.error && typeof status.error === 'object') return badge('Error', 'degraded')
  if (ready === false) return badge('Not ready', 'unknown')
  if (isVolumeSnapshot(data) ? !nonEmptyString(status?.boundVolumeSnapshotContentName) : !status || typeof status !== 'object') return badge('Pending', 'unknown')
  return badge('Readiness unknown', 'unknown')
}

function nonEmptyString(v: unknown): string | undefined {
  return typeof v === 'string' && v ? v : undefined
}

export function getVolumeSnapshotErrorMessage(data: any): string | undefined {
  const error = data?.status?.error
  if (!error || typeof error !== 'object') return undefined
  return nonEmptyString(typeof error.message === 'string' ? error.message.trim() : undefined) ?? 'The controller recorded an error without a message.'
}

export type VolumeSnapshotSource =
  | { kind: 'PersistentVolumeClaim'; name: string }
  | { kind: 'VolumeSnapshotContent'; name: string }

/** The requested source. It is not proof of binding; that is status.boundVolumeSnapshotContentName. */
export function getVolumeSnapshotSource(data: any): VolumeSnapshotSource | undefined {
  const source = data?.spec?.source
  const pvc = nonEmptyString(source?.persistentVolumeClaimName)
  if (pvc) return { kind: 'PersistentVolumeClaim', name: pvc }
  const content = nonEmptyString(source?.volumeSnapshotContentName)
  if (content) return { kind: 'VolumeSnapshotContent', name: content }
  return undefined
}

const BINARY_UNITS = ['Ei', 'Pi', 'Ti', 'Gi', 'Mi', 'Ki'] as const

/** Exact binary quantities read the way Kubernetes writes them ("1Gi"); anything else is rounded. */
export function formatSnapshotBytes(bytes: number): string {
  for (const [i, unit] of BINARY_UNITS.entries()) {
    const size = 1024 ** (BINARY_UNITS.length - i)
    if (bytes >= size && bytes % size === 0) return `${bytes / size}${unit}`
  }
  return formatBytes(bytes)
}

/**
 * The snapshot reports restore size as a quantity, its content as a byte
 * count. Missing means the driver did not report it, never zero.
 */
export function getVolumeSnapshotRestoreSize(data: any): { text: string; bytes: number } | undefined {
  const size = data?.status?.restoreSize
  if (isVolumeSnapshotContent(data)) {
    return Number.isInteger(size) && size >= 0 ? { text: formatSnapshotBytes(size), bytes: size } : undefined
  }
  const bytes = typeof size === 'string' ? parseQuantity(size) : null
  return bytes === null ? undefined : { text: size, bytes }
}

/**
 * When the storage system cut the snapshot. A content reports nanoseconds
 * since the epoch, a snapshot an RFC 3339 time.
 */
export function getVolumeSnapshotCreationTime(data: any): string | undefined {
  const raw = data?.status?.creationTime
  if (isVolumeSnapshotContent(data)) {
    if (typeof raw !== 'number' || !Number.isFinite(raw) || raw <= 0) return undefined
    return new Date(Math.floor(raw / 1e6)).toISOString()
  }
  return nonEmptyString(raw) && !Number.isNaN(Date.parse(raw)) ? raw : undefined
}

export function isDefaultVolumeSnapshotClass(data: any): boolean {
  return data?.metadata?.annotations?.[DEFAULT_CLASS_ANNOTATION] === 'true'
}

/** The snapshot a content names. Without a UID the snapshot controller has not bound them yet. */
export function getVolumeSnapshotContentRef(data: any): { name: string; namespace: string; bound: boolean } | undefined {
  const ref = data?.spec?.volumeSnapshotRef
  const name = nonEmptyString(ref?.name)
  if (!name) return undefined
  return { name, namespace: nonEmptyString(ref?.namespace) ?? '', bound: !!nonEmptyString(ref?.uid) }
}

function describeSource(data: any): string {
  const source = getVolumeSnapshotSource(data)
  if (!source) return ''
  return source.kind === 'PersistentVolumeClaim' ? source.name : `${source.name} (pre-provisioned)`
}

export function getVolumeSnapshotColumnValue(data: any, column: string): string | undefined {
  if (isVolumeSnapshot(data)) {
    switch (column) {
      case 'status': return getVolumeSnapshotStatus(data).text
      case 'snapshotSource': return describeSource(data)
      case 'restoreSize': return getVolumeSnapshotRestoreSize(data)?.text ?? ''
      case 'snapshotClass': return nonEmptyString(data.spec?.volumeSnapshotClassName) ?? ''
      case 'snapshotContent': return nonEmptyString(data.status?.boundVolumeSnapshotContentName) ?? ''
      case 'snapshotTaken': {
        const at = getVolumeSnapshotCreationTime(data)
        return at ? formatAge(at) : ''
      }
      default: return undefined
    }
  }
  if (isVolumeSnapshotContent(data)) {
    switch (column) {
      case 'status': return getVolumeSnapshotStatus(data).text
      case 'snapshotRef': {
        const ref = getVolumeSnapshotContentRef(data)
        return ref ? `${ref.namespace ? `${ref.namespace}/` : ''}${ref.name}` : ''
      }
      case 'snapshotDriver': return nonEmptyString(data.spec?.driver) ?? ''
      case 'deletionPolicy': return nonEmptyString(data.spec?.deletionPolicy) ?? ''
      case 'restoreSize': return getVolumeSnapshotRestoreSize(data)?.text ?? ''
      case 'snapshotClass': return nonEmptyString(data.spec?.volumeSnapshotClassName) ?? ''
      default: return undefined
    }
  }
  if (isVolumeSnapshotClass(data)) {
    switch (column) {
      case 'snapshotDriver': return nonEmptyString(data.driver) ?? ''
      case 'deletionPolicy': return nonEmptyString(data.deletionPolicy) ?? ''
      case 'defaultClass': return isDefaultVolumeSnapshotClass(data) ? 'Yes' : 'No'
      default: return undefined
    }
  }
  return undefined
}

/** Numeric sort keys for the size and time columns; -1 sorts unreported values first. */
export function getVolumeSnapshotSortValue(data: any, column: string): string | number | undefined {
  const value = getVolumeSnapshotColumnValue(data, column)
  if (value === undefined) return undefined
  if (column === 'restoreSize') return getVolumeSnapshotRestoreSize(data)?.bytes ?? -1
  if (column === 'snapshotTaken') {
    const at = getVolumeSnapshotCreationTime(data)
    return at ? Date.parse(at) : -1
  }
  return value
}
