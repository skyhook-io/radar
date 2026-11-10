// The parts of a barman-cloud destination an on-call engineer checks when WAL
// archiving fails: where it writes and which Secrets hold its credentials.
// Secrets are named, never read.

export interface CNPGArchiveDestination {
  /** destinationPath, e.g. s3://bucket/prefix */
  path?: string
  endpointURL?: string
  /** Each Secret key the destination reads its credentials from. */
  secrets: { secret: string; key?: string; what: string }[]
  /** Credentials come from the workload's identity instead of a Secret. */
  identity?: string
  /** Where this configuration is declared. */
  declaredIn: string
}

const SECRET_FIELDS: [string, string, string][] = [
  ['s3Credentials', 'accessKeyId', 'S3 access key ID'],
  ['s3Credentials', 'secretAccessKey', 'S3 secret access key'],
  ['s3Credentials', 'region', 'S3 region'],
  ['s3Credentials', 'sessionToken', 'S3 session token'],
  ['azureCredentials', 'connectionString', 'Azure connection string'],
  ['azureCredentials', 'storageAccount', 'Azure storage account'],
  ['azureCredentials', 'storageKey', 'Azure storage key'],
  ['azureCredentials', 'storageSasToken', 'Azure SAS token'],
  ['googleCredentials', 'applicationCredentials', 'Google application credentials'],
]

/** Reads a barman-cloud object store configuration (in-tree `spec.backup.barmanObjectStore` or an ObjectStore's `spec.configuration`). */
export function cnpgArchiveDestination(config: any, declaredIn: string): CNPGArchiveDestination {
  const secrets: CNPGArchiveDestination['secrets'] = []
  for (const [group, field, what] of SECRET_FIELDS) {
    const ref = config?.[group]?.[field]
    if (typeof ref?.name === 'string' && ref.name) secrets.push({ secret: ref.name, key: typeof ref.key === 'string' ? ref.key : undefined, what })
  }
  const ca = config?.endpointCA
  if (typeof ca?.name === 'string' && ca.name) secrets.push({ secret: ca.name, key: typeof ca.key === 'string' ? ca.key : undefined, what: 'endpoint CA bundle' })
  const identity = config?.s3Credentials?.inheritFromIAMRole
    ? 'the Pod’s IAM role (inheritFromIAMRole)'
    : config?.azureCredentials?.inheritFromAzureAD
      ? 'the Pod’s Azure AD workload identity (inheritFromAzureAD)'
      : config?.googleCredentials?.gkeEnvironment
        ? 'the GKE workload identity (gkeEnvironment)'
        : undefined
  return {
    path: typeof config?.destinationPath === 'string' ? config.destinationPath : undefined,
    endpointURL: typeof config?.endpointURL === 'string' ? config.endpointURL : undefined,
    secrets,
    identity,
    declaredIn,
  }
}

/**
 * When archiving resumed, at the latest: the later of the last failure the
 * instance manager saw and the ContinuousArchiving condition turning True. A
 * Backup that started after the last failure saw no archive failure while it
 * ran. NaN when neither is known.
 */
export function resumeBoundary(cluster: any, failedAt: number): number {
  const conds = cluster?.status?.conditions
  const c = Array.isArray(conds) ? conds.find((x: any) => x?.type === 'ContinuousArchiving') : null
  const trueSince = c?.status === 'True' ? Date.parse(c.lastTransitionTime ?? '') : NaN
  if (!Number.isFinite(trueSince)) return failedAt
  return Number.isFinite(failedAt) && failedAt > trueSince ? failedAt : trueSince
}

/** The method a Backup ran with; CloudNativePG defaults an unset one to barmanObjectStore. */
export function backupMethod(b: any): string {
  return b?.status?.method || b?.spec?.method || 'barmanObjectStore'
}

export type CNPGWALArchiver = { method: 'plugin'; plugin: string } | { method: 'barmanObjectStore' }

const WAL_FILE = /^[0-9A-F]{24}$/i

/**
 * Whether WAL file `a` comes after `b`. The last 16 hex digits are the
 * segment's position, which keeps increasing across timelines; null when
 * either name is not a WAL file name.
 */
export function walAfter(a: unknown, b: unknown): boolean | null {
  if (typeof a !== 'string' || typeof b !== 'string' || !WAL_FILE.test(a) || !WAL_FILE.test(b)) return null
  return a.slice(8).toUpperCase() > b.slice(8).toUpperCase()
}

/**
 * Whether a restore can start after the failure without any WAL that failed
 * to archive. A Backup that started after archiving resumed can still begin
 * earlier in the WAL — CloudNativePG backs up a standby by default, and a
 * lagging one begins where it has replayed to — so its own beginWal decides,
 * against the last WAL the instance manager failed to archive.
 */
export type CNPGRecoveryBase =
  | { state: 'unread' }
  | { state: 'none' }
  | { state: 'verified'; backup: any; failedWal: string }
  | { state: 'beginsBefore'; backup: any; failedWal: string }
  /** `missing` says which side of the comparison could not be read. */
  | { state: 'unverifiable'; backup: any; missing: 'failedWal' | 'beginWal' }

export function cnpgRecoveryBase(
  cluster: { namespace: string; name: string },
  backups: any[] | null,
  boundary: number,
  archiver: CNPGWALArchiver,
  failedWal?: string,
): CNPGRecoveryBase {
  if (backups === null) return { state: 'unread' }
  const candidates = backupsAfterResume(cluster, backups, boundary, archiver)
  if (candidates.length === 0) return { state: 'none' }
  if (!failedWal || walAfter(failedWal, failedWal) === null) return { state: 'unverifiable', backup: candidates[0], missing: 'failedWal' }
  const verified = candidates.find((b) => walAfter(b?.status?.beginWal, failedWal) === true)
  if (verified) return { state: 'verified', backup: verified, failedWal }
  const newest = candidates[0]
  return walAfter(newest?.status?.beginWal, failedWal) === false
    ? { state: 'beginsBefore', backup: newest, failedWal }
    : { state: 'unverifiable', backup: newest, missing: 'beginWal' }
}

/**
 * Completed Backups of this cluster that started after `boundary`, newest
 * first, that a restore can start from with this cluster's WAL archive: one
 * written by the same archiver, or a volume snapshot (restored by replaying
 * WAL from the archive).
 */
export function backupsAfterResume(cluster: { namespace: string; name: string }, backups: any[], boundary: number, archiver: CNPGWALArchiver): any[] {
  const sameArchive = (b: any) => {
    const m = backupMethod(b)
    if (m === 'volumeSnapshot') return true
    if (archiver.method === 'plugin') return m === 'plugin' && b?.spec?.pluginConfiguration?.name === archiver.plugin
    return m === 'barmanObjectStore'
  }
  return backups
    .filter(
      (b) =>
        String(b?.apiVersion ?? '').startsWith('postgresql.cnpg.io/') &&
        b?.metadata?.namespace === cluster.namespace &&
        b?.spec?.cluster?.name === cluster.name &&
        b?.status?.phase === 'completed' &&
        sameArchive(b) &&
        Date.parse(b?.status?.startedAt ?? '') > boundary,
    )
    .sort((a, b) => Date.parse(b.status.startedAt) - Date.parse(a.status.startedAt))
}
