import { cnpgClusterPlugins, cnpgPluginPhase, formatAge, getCNPGClusterBarmanPlugin, StatusDot, toneTextClass, type CNPGFleetRow } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import type { CNPGRuntimeInstance } from '../../api/cnpg'
import { backupAfterResume, cnpgArchiveDestination, resumeBoundary } from './archivingRepair'

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
  backups,
  onOpenLogs,
  onOpenOperator,
  onInspect,
}: {
  row: CNPGFleetRow
  primary?: CNPGRuntimeInstance
  objectStores: any[]
  /** This namespace's CNPG Backups, or null when they could not be read. */
  backups: any[] | null
  onOpenLogs?: (pod: string, container: string) => void
  onOpenOperator?: () => void
  onInspect: (r: SelectedResource) => void
}) {
  const wal = row.protection.walArchiving
  const arch = primary && (primary.status.state === 'ok' || primary.status.state === 'partial') ? primary.status.archiving : undefined
  const failedAt = arch?.lastFailedAt ? Date.parse(arch.lastFailedAt) : NaN
  const archivedAt = arch?.lastArchivedAt ? Date.parse(arch.lastArchivedAt) : NaN
  const failingNow = wal.tone === 'unhealthy' || (Number.isFinite(failedAt) && (!Number.isFinite(archivedAt) || failedAt > archivedAt))
  const resumed = !failingNow && wal.tone === 'healthy' && Number.isFinite(failedAt) && Number.isFinite(archivedAt) && archivedAt > failedAt && Date.now() - failedAt < RESUMED_WINDOW_MS
  if (!failingNow && !resumed) return null

  // A plugin archives WAL only when it is marked isWALArchiver; otherwise the
  // in-tree barmanObjectStore (if any) does, from the postgres container.
  const barman = getCNPGClusterBarmanPlugin(row.cluster)
  const plugin = barman?.isWALArchiver ? barman : null
  const storeName = plugin?.barmanObjectName
  const store = storeName ? objectStores.find((s) => s?.metadata?.namespace === row.namespace && s?.metadata?.name === storeName) : undefined
  const inTree = row.cluster?.spec?.backup?.barmanObjectStore
  const dest = store
    ? cnpgArchiveDestination(store.spec?.configuration, `ObjectStore ${storeName}`)
    : inTree
      ? cnpgArchiveDestination(inTree, 'the Cluster’s spec.backup.barmanObjectStore')
      : undefined
  const archiverContainer = plugin ? 'plugin-barman-cloud' : 'postgres'
  const pluginPhase = cnpgPluginPhase(row.cluster)
  const primaryPod = primary?.pod ?? row.cluster?.status?.currentPrimary
  const lastBackup = row.protection.lastSuccessfulBackup
  const fresh = resumed ? backupAfterResume(row, backups, resumeBoundary(row.cluster, failedAt)) : undefined

  return (
    <section className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <h3 className={`text-sm font-semibold ${failingNow ? toneTextClass('unhealthy') : toneTextClass('healthy')}`}>
        {failingNow ? 'WAL archiving is failing' : 'WAL archiving resumed'}
      </h3>
      <div className="mt-1.5 space-y-1.5 text-sm">
        {failingNow && (
          <p className="text-theme-text-primary">
            {wal.tone === 'unhealthy' ? (
              wal.detail ? (
                <>
                  The operator reports: <span className="font-mono text-xs">{wal.detail}</span>
                </>
              ) : (
                'The operator reports ContinuousArchiving False without a message.'
              )
            ) : (
              `The primary’s instance manager reports a failed archive after its last archived WAL${wal.tone === 'healthy' ? '; the operator’s ContinuousArchiving condition still reads True' : ''}.`
            )}
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
              {dest.identity && (
                <>
                  {dest.identity}
                  {dest.secrets.length > 0 && ' · '}
                </>
              )}
              {!dest.identity && dest.secrets.length === 0
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
            The primary archives from its <span className="font-mono">{archiverContainer}</span> container; the failing command’s output is in its logs.{' '}
            {onOpenLogs && (
              <button type="button" className="text-accent-text hover:underline" onClick={() => onOpenLogs(primaryPod, archiverContainer)}>
                {archiverContainer} logs of {primaryPod} →
              </button>
            )}
          </p>
        )}
        {failingNow && pluginPhase && (
          <p className="text-xs text-theme-text-secondary">
            Separately, the Cluster’s phase says the operator {pluginPhase === 'unknownPlugin' ? 'does not know a plugin this cluster requires' : 'hit an error talking to one of this cluster’s plugins'} (spec.plugins:{' '}
            {cnpgClusterPlugins(row.cluster).join(', ') || 'none listed'}), so it cannot reconcile this cluster until that clears.{' '}
            {onOpenOperator && (
              <button type="button" className="text-accent-text hover:underline" onClick={onOpenOperator}>
                Operator and plugins →
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
            <span className="mt-1"><StatusDot tone={fresh && fresh !== 'unread' ? 'healthy' : 'unknown'} /></span>
            <span className={fresh && fresh !== 'unread' ? 'text-theme-text-primary' : 'text-theme-text-secondary'}>
              A base backup starts after archiving {failingNow ? 'resumes' : 'resumed'} (Back up now, above), so recovery does not depend on WAL from before the gap.{' '}
              {failingNow
                ? `Not yet. Newest successful backup: ${lastBackup.at ? `${formatAge(lastBackup.at)} ago` : lastBackup.text.toLowerCase()}.`
                : fresh === 'unread'
                  ? 'Radar cannot read this namespace’s Backups, so it cannot tell.'
                  : fresh
                    ? `Done: Backup ${fresh.metadata?.name}${fresh.spec?.method ? ` (${fresh.spec.method})` : ''} started ${formatAge(fresh.status.startedAt)} ago and completed.`
                    : 'No completed Backup of this cluster started after that yet.'}
            </span>
          </li>
        </ol>
      </div>
    </section>
  )
}
