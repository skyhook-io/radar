import { formatAge, getCNPGClusterBarmanPlugin, StatusDot, toneTextClass, type CNPGFleetRow } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import type { CNPGRuntimeInstance } from '../../api/cnpg'
import { cnpgArchiveDestination } from './archivingRepair'

const RESUMED_WINDOW_MS = 24 * 60 * 60 * 1000

/**
 * WAL archiving, as a repair: why it fails, where the destination and its
 * credentials are declared, where the archiver logs, then whether archiving
 * resumed and a fresh base backup exists. Shown while archiving fails, and
 * for a day after it resumes so the fix can be confirmed here.
 */
export function CNPGArchivingRepair({
  row,
  primary,
  objectStores,
  onOpenLogs,
  onInspect,
}: {
  row: CNPGFleetRow
  primary?: CNPGRuntimeInstance
  objectStores: any[]
  onOpenLogs?: (pod: string) => void
  onInspect: (r: SelectedResource) => void
}) {
  const wal = row.protection.walArchiving
  const arch = primary && (primary.status.state === 'ok' || primary.status.state === 'partial') ? primary.status.archiving : undefined
  const failedAt = arch?.lastFailedAt ? Date.parse(arch.lastFailedAt) : NaN
  const archivedAt = arch?.lastArchivedAt ? Date.parse(arch.lastArchivedAt) : NaN
  const failingNow = wal.tone === 'unhealthy' || (Number.isFinite(failedAt) && (!Number.isFinite(archivedAt) || failedAt > archivedAt))
  const resumed = !failingNow && wal.tone === 'healthy' && Number.isFinite(failedAt) && Number.isFinite(archivedAt) && archivedAt > failedAt && Date.now() - failedAt < RESUMED_WINDOW_MS
  if (!failingNow && !resumed) return null

  const plugin = getCNPGClusterBarmanPlugin(row.cluster)
  const storeName = plugin?.barmanObjectName
  const store = storeName ? objectStores.find((s) => s?.metadata?.namespace === row.namespace && s?.metadata?.name === storeName) : undefined
  const inTree = row.cluster?.spec?.backup?.barmanObjectStore
  const dest = store
    ? cnpgArchiveDestination(store.spec?.configuration, `ObjectStore ${storeName}`)
    : inTree
      ? cnpgArchiveDestination(inTree, 'the Cluster’s spec.backup.barmanObjectStore')
      : undefined
  const archiver = plugin ? 'its plugin-barman-cloud container' : 'its postgres container'
  const primaryPod = primary?.pod ?? row.cluster?.status?.currentPrimary
  const lastBackup = row.protection.lastSuccessfulBackup

  return (
    <section className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <h3 className={`text-sm font-semibold ${failingNow ? toneTextClass('unhealthy') : toneTextClass('healthy')}`}>
        {failingNow ? 'WAL archiving is failing' : 'WAL archiving resumed'}
      </h3>
      <div className="mt-1.5 space-y-1.5 text-sm">
        {failingNow && (
          <p className="text-theme-text-primary">
            {wal.text.includes('·') ? wal.text.slice(wal.text.indexOf('·') + 1).trim() : 'The operator reports ContinuousArchiving False without a message.'}
          </p>
        )}
        {arch && (
          <p className="text-xs text-theme-text-secondary">
            {arch.lastArchivedAt ? `Last archived ${formatAge(arch.lastArchivedAt)} ago${arch.lastArchivedWal ? ` (${arch.lastArchivedWal})` : ''}` : 'Nothing archived since the instance manager started'}
            {arch.lastFailedAt ? ` · last failure ${formatAge(arch.lastFailedAt)} ago${arch.lastFailedWal ? ` (${arch.lastFailedWal})` : ''}` : ''}
            {arch.readyWalFiles !== undefined ? ` · ${arch.readyWalFiles} files waiting` : ''} · from the primary’s instance manager
          </p>
        )}
        {failingNow && dest && (
          <div className="text-xs text-theme-text-secondary">
            <div>
              Destination <span className="font-mono text-theme-text-primary">{dest.path ?? 'path not declared'}</span>
              {dest.endpointURL && <> via <span className="font-mono">{dest.endpointURL}</span></>}, declared in{' '}
              {store ? (
                <button type="button" className="text-accent-text hover:underline" onClick={() => onInspect({ kind: 'objectstores', group: 'barmancloud.cnpg.io', namespace: row.namespace, name: storeName! })}>
                  {dest.declaredIn}
                </button>
              ) : (
                dest.declaredIn
              )}
              .
            </div>
            <div className="mt-0.5">
              Credentials:{' '}
              {dest.identity
                ? dest.identity
                : dest.secrets.length === 0
                  ? 'none referenced'
                  : dest.secrets.map((s, i) => (
                      <span key={`${s.secret}/${s.key}/${s.what}`}>
                        {i > 0 && ' · '}
                        {s.what} in Secret{' '}
                        <button type="button" className="font-mono text-accent-text hover:underline" onClick={() => onInspect({ kind: 'secrets', group: '', namespace: row.namespace, name: s.secret })}>
                          {s.secret}
                        </button>
                        {s.key ? <span className="font-mono">/{s.key}</span> : null}
                      </span>
                    ))}
              . Radar names them and does not read them.
            </div>
          </div>
        )}
        {failingNow && primaryPod && (
          <p className="text-xs text-theme-text-secondary">
            The primary archives from {archiver}; the failing command’s output is in its logs.{' '}
            {onOpenLogs && (
              <button type="button" className="text-accent-text hover:underline" onClick={() => onOpenLogs(primaryPod)}>
                Logs of {primaryPod} →
              </button>
            )}
          </p>
        )}
        <ol className="mt-1 space-y-1 text-xs">
          <li className="flex items-start gap-2">
            <span className="mt-1"><StatusDot tone={failingNow ? 'unknown' : 'healthy'} /></span>
            <span className={failingNow ? 'text-theme-text-secondary' : 'text-theme-text-primary'}>
              Archiving resumes: the operator reports ContinuousArchiving True and a WAL file archives after the last failure.
              {failingNow ? ' Not yet.' : ` Done${arch?.lastArchivedAt ? `, ${formatAge(arch.lastArchivedAt)} ago` : ''}.`}
            </span>
          </li>
          <li className="flex items-start gap-2">
            <span className="mt-1"><StatusDot tone={!failingNow && lastBackup.at && Number.isFinite(failedAt) && Date.parse(lastBackup.at) > failedAt ? 'healthy' : 'unknown'} /></span>
            <span className="text-theme-text-secondary">
              A base backup completes after archiving resumed (Back up now, above), so recovery does not depend on WAL from before the gap. Newest successful backup:{' '}
              {lastBackup.at ? `${formatAge(lastBackup.at)} ago` : lastBackup.text.toLowerCase()}.
            </span>
          </li>
        </ol>
      </div>
    </section>
  )
}
