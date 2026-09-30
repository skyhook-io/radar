import { useMemo, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import yaml from 'yaml'
import { ActionConfirmDialog, isApiGroup, toneTextClass, Tooltip, type HealthLevel } from '@skyhook-io/k8s-ui'
import { useCNPGRuntime, useCNPGWorkspace } from '../../../api/cnpg'
import { useCNPGRestoreCapability } from '../../../api/cnpg-recovery'
import { useConnection } from '../../../context/ConnectionContext'
import { CreateResourceDialog } from '../../shared/CreateResourceDialog'
import { useToast } from '../../ui/Toast'
import { cnpgClusterFullPath } from '../paths'
import { trackCNPGOperation } from '../operations/store'
import { CNPG_RESTORE_OPERATION } from './restoreOperation'
import {
  buildRestoreManifest,
  describeSource,
  formatLocal,
  formatUTC,
  pitrWarnings,
  preflightFacts,
  recoveryEvidenceFor,
  restoreManifestHeader,
  restoreSourceForBackup,
  restoreSourcesFor,
  restoreSourcesForStore,
  sourceClusterFor,
  restorePermission,
  sourcePinsBackup,
  targetIsoFrom,
  type EvidencePoint,
  type RestoreSource,
  type RestoreTarget,
} from './restoreModel'

/** Where the restore was started from; decides the default source. */
export type CNPGRestoreEntry = { kind: 'cluster'; name: string } | { kind: 'backup'; name: string } | { kind: 'objectStore'; name: string }

const NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
const FIELD = 'rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm text-theme-text-primary'

function When({ point, empty }: { point?: EvidencePoint & { wal?: string }; empty: string }) {
  if (!point) return <span className="text-theme-text-tertiary">{empty}</span>
  return (
    <>
      <div className="font-mono text-[12.5px] text-theme-text-primary">{formatUTC(point.at)}</div>
      <div className="text-[11px] text-theme-text-tertiary">
        {formatLocal(point.at)}
        {point.wal ? ` · ${point.wal}` : ''}
      </div>
      <div className="text-[11px] text-theme-text-tertiary">{point.source}</div>
    </>
  )
}

function EvidenceRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[8.5rem_minmax(0,1fr)] gap-x-3 py-1.5">
      <div className="text-xs text-theme-text-secondary">{label}</div>
      <div className="min-w-0">{children}</div>
    </div>
  )
}

/**
 * Restore builds a new Cluster manifest, shows the evidence for the recovery
 * target and what the manifest copies from the source, and hands it to
 * Radar's create flow (server dry-run, strict create). After the create the
 * user is offered the new Cluster's page, which follows the restore.
 */
export function CNPGRestoreDialog({ namespace, entry, onClose }: { namespace: string; entry: CNPGRestoreEntry; onClose: () => void }) {
  const navigate = useNavigate()
  const { connection } = useConnection()
  const { showSuccess } = useToast()
  const workspace = useCNPGWorkspace([namespace])
  const objects = workspace.data?.objects
  const clusters = useMemo(() => (objects?.clusters ?? []).filter((c: any) => isApiGroup(c.apiVersion, 'postgresql.cnpg.io')), [objects])
  const backups = useMemo(() => (objects?.backups ?? []).filter((b: any) => isApiGroup(b.apiVersion, 'postgresql.cnpg.io')), [objects])
  const stores = objects?.objectStores ?? []

  const sources = useMemo<RestoreSource[]>(() => {
    if (!objects) return []
    if (entry.kind === 'cluster') {
      const c = clusters.find((x: any) => x.metadata?.namespace === namespace && x.metadata?.name === entry.name)
      return c ? restoreSourcesFor(c, backups) : []
    }
    if (entry.kind === 'backup') {
      const b = backups.find((x: any) => x.metadata?.namespace === namespace && x.metadata?.name === entry.name)
      const owner = clusters.find((x: any) => x.metadata?.namespace === namespace && x.metadata?.name === b?.spec?.cluster?.name) ?? null
      const s = b ? restoreSourceForBackup(b, owner) : null
      return s ? [s] : []
    }
    const store = stores.find((x: any) => x.metadata?.namespace === namespace && x.metadata?.name === entry.name)
    return store ? restoreSourcesForStore(store) : []
  }, [objects, clusters, backups, stores, entry, namespace])

  const [sourceIdx, setSourceIdx] = useState(0)
  const source = sources[sourceIdx]
  const sourceCluster = useMemo(() => {
    if (!source) return null
    if (entry.kind === 'cluster') return clusters.find((c: any) => c.metadata?.namespace === namespace && c.metadata?.name === entry.name) ?? null
    if (source.kind === 'backup' || (source.kind === 'objectStore' && source.backupName)) {
      const b = backups.find((x: any) => x.metadata?.name === (source.kind === 'backup' ? source.backup : source.backupName))
      return clusters.find((c: any) => c.metadata?.namespace === namespace && c.metadata?.name === b?.spec?.cluster?.name) ?? null
    }
    return sourceClusterFor(source, clusters, namespace)
  }, [source, entry, clusters, backups, namespace])
  const sourceName = sourceCluster?.metadata?.name as string | undefined
  const runtime = useCNPGRuntime(namespace, sourceName ?? '', !!sourceName)

  const defaultName = `${sourceName ?? (source?.kind === 'objectStore' ? source.serverName : entry.name)}-restore`
  const [newName, setNewName] = useState<string | null>(null)
  const name = newName ?? defaultName
  const pinned = sourcePinsBackup(source)
  const [targetKind, setTargetKind] = useState<RestoreTarget['kind'] | null>(null)
  const effectiveKind = targetKind ?? (pinned ? 'backupEnd' : 'latest')
  const [timeValue, setTimeValue] = useState('')
  const [zone, setZone] = useState<'utc' | 'local'>('utc')
  const [manifest, setManifest] = useState<string | null>(null)
  const restoreCap = useCNPGRestoreCapability(namespace)

  const evidence = useMemo(
    () => recoveryEvidenceFor(source, { sourceCluster, stores, backups, runtime: runtime.data, namespace }),
    [source, sourceCluster, stores, backups, runtime.data, namespace],
  )
  const targetIso = effectiveKind === 'time' ? targetIsoFrom(timeValue, zone) : null
  const warnings = effectiveKind === 'time' && targetIso ? pitrWarnings(targetIso, evidence) : []
  const facts = preflightFacts(sourceCluster)
  const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone
  const serverName = source && source.kind !== 'backup' ? source.serverName : undefined

  if (manifest) {
    return (
      <CreateResourceDialog
        open
        onClose={onClose}
        initialYaml={manifest}
        initialMode="create"
        lockMode
        title={`Restore into a new cluster ${name}`}
        onCreated={(created) => {
          onClose()
          trackCNPGOperation({
            kind: CNPG_RESTORE_OPERATION,
            label: `Restore into ${created.name}`,
            context: connection.context,
            namespace: created.namespace || namespace,
            cluster: created.name,
            baseline: { source: source ? describeSource(source) : undefined },
          })
          const path = cnpgClusterFullPath(created.namespace || namespace, created.name, connection.context || undefined)
          showSuccess(`Cluster ${created.name} created`, 'The operator is restoring it from backup.', { label: 'Follow the restore', onClick: () => navigate(path) })
        }}
      />
    )
  }

  const permission = restorePermission(namespace, restoreCap.data, restoreCap.error)
  const disabledReason =
    permission.blocked ??
    (!workspace.isLoading && sources.length === 0
      ? entry.kind === 'backup'
        ? 'This Backup cannot be restored: it has not completed, or its object store and backup ID are not recorded.'
        : entry.kind === 'objectStore'
          ? 'This ObjectStore reports no server with backups yet.'
          : 'This cluster has no backup destination and no completed Backup to restore from.'
      : undefined)
  const incompleteReason =
    permission.pending ??
    (workspace.isLoading
      ? 'Loading backups and object stores…'
      : !NAME_RE.test(name)
        ? 'The new name must be a valid Kubernetes object name.'
        : clusters.some((c: any) => c.metadata?.namespace === namespace && c.metadata?.name === name)
          ? `A Cluster named ${name} already exists in ${namespace}.`
          : effectiveKind === 'time' && !targetIso
            ? 'Enter the point in time to recover to.'
            : undefined)

  return (
    <ActionConfirmDialog
      open
      size="wide"
      onClose={onClose}
      onConfirm={() => {
        if (!source) return
        const target: RestoreTarget = effectiveKind === 'time' && targetIso ? { kind: 'time', iso: targetIso } : effectiveKind === 'backupEnd' ? { kind: 'backupEnd' } : { kind: 'latest' }
        const m = buildRestoreManifest({ sourceCluster, source, namespace, newName: name, target })
        // The apiserver reads YAML 1.1, where unquoted on/off/yes are booleans (postgresql parameters are strings).
        setManifest(restoreManifestHeader(describeSource(source), serverName, sourceName ?? null) + yaml.stringify(m, { version: '1.1' }))
      }}
      title="Restore to a new cluster"
      subject={{ kind: entry.kind === 'cluster' ? 'Cluster' : entry.kind === 'backup' ? 'Backup' : 'ObjectStore', namespace, name: entry.name }}
      context={connection.context || undefined}
      effect="Creates a new Cluster that bootstraps from backups. Nothing existing is changed; the source keeps running."
      confirmLabel="Review manifest"
      warnings={[
        'The new cluster has no WAL archiving or backups until you configure them.',
        ...(serverName ? [`If you add archiving later, do not reuse server name "${serverName}": the new cluster would write into the archive it restores from.`] : []),
        ...warnings,
        ...(permission.unchecked ? [permission.unchecked] : []),
      ]}
      disabledReason={disabledReason}
      incompleteReason={incompleteReason}
    >
      <div className="grid gap-5 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <div className="space-y-3">
          <div className="grid grid-cols-[7.5rem_minmax(0,1fr)] items-center gap-x-3 gap-y-2">
            <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-restore-source">Restore from</label>
            <select id="cnpg-restore-source" value={sourceIdx} onChange={(e) => { setSourceIdx(Number(e.target.value)); setTargetKind(null) }} className={FIELD}>
              {sources.map((s, i) => <option key={i} value={i}>{describeSource(s)}</option>)}
            </select>
            <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-restore-name">New cluster</label>
            <input id="cnpg-restore-name" value={name} onChange={(e) => setNewName(e.target.value)} className={`${FIELD} font-mono`} />
          </div>
          <fieldset className="space-y-1.5">
            <legend className="mb-1 text-xs text-theme-text-secondary">Recover to</legend>
            {pinned && (
              <label className="flex items-center gap-2 text-sm text-theme-text-primary">
                <input type="radio" name="cnpg-restore-target" checked={effectiveKind === 'backupEnd'} onChange={() => setTargetKind('backupEnd')} />
                The end of this backup{source?.kind === 'objectStore' && source.backupEnd ? ` (${formatUTC(source.backupEnd)})` : ''}
              </label>
            )}
            <label className="flex items-center gap-2 text-sm text-theme-text-primary">
              <input type="radio" name="cnpg-restore-target" checked={effectiveKind === 'latest'} onChange={() => setTargetKind('latest')} />
              The latest archived WAL
            </label>
            <label className="flex items-center gap-2 text-sm text-theme-text-primary">
              <input type="radio" name="cnpg-restore-target" checked={effectiveKind === 'time'} onChange={() => setTargetKind('time')} />
              A point in time
            </label>
            {effectiveKind === 'time' && (
              <div className="ml-6 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <input
                    aria-label="Target time"
                    type="datetime-local"
                    step={1}
                    value={timeValue}
                    onChange={(e) => setTimeValue(e.target.value)}
                    className={FIELD}
                  />
                  <select aria-label="Time zone of the target" value={zone} onChange={(e) => setZone(e.target.value as 'utc' | 'local')} className={FIELD}>
                    <option value="utc">UTC</option>
                    <option value="local">{localZone}</option>
                  </select>
                </div>
                <div className="text-[11px] text-theme-text-tertiary">
                  {targetIso ? <>Target: <span className="font-mono">{targetIso}</span> · {formatLocal(targetIso)}</> : 'The target is written to the manifest in UTC.'}
                </div>
              </div>
            )}
            {pinned && effectiveKind !== 'backupEnd' && (
              <div className="ml-6 text-[11px] text-theme-text-tertiary">Starts from this backup and replays archived WAL after it.</div>
            )}
          </fieldset>
        </div>

        <div className="rounded-lg border border-theme-border bg-theme-base px-3 py-2">
          <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">What the source holds</div>
          <EvidenceRow label="First recoverability point">
            <When point={evidence.firstPoint} empty={source?.kind === 'backup' ? 'This backup' : 'Not reported'} />
          </EvidenceRow>
          <EvidenceRow label="Last successful backup">
            <When point={evidence.lastBackup} empty="None observed" />
          </EvidenceRow>
          <EvidenceRow label="WAL archiving">
            <Tooltip content={evidence.archiving.detail} disabled={!evidence.archiving.detail}>
              <span className={toneTextClass(evidence.archiving.tone as HealthLevel)}>{evidence.archiving.text}</span>
            </Tooltip>
            <div className="text-[11px] text-theme-text-tertiary">{evidence.archiving.source}</div>
          </EvidenceRow>
          <EvidenceRow label="Last archived WAL">
            <When point={evidence.lastArchived} empty={sourceName ? 'Not reported' : 'Unknown'} />
          </EvidenceRow>
          {evidence.lastArchiveFailure && (
            <EvidenceRow label="Last archive failure">
              <When point={evidence.lastArchiveFailure} empty="" />
            </EvidenceRow>
          )}
          {evidence.gaps.length > 0 && (
            <ul className="mt-1 list-disc space-y-0.5 pl-4 text-[11px] text-theme-text-tertiary">
              {evidence.gaps.map((g) => <li key={g}>{g}</li>)}
            </ul>
          )}
        </div>
      </div>

      <div className="mt-4">
        <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
          {sourceName ? `Copied from ${sourceName} — edit in the manifest` : 'No source cluster found — fill these in the manifest'}
        </div>
        <dl className="grid grid-cols-[9rem_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
          {facts.map((f) => (
            <div key={f.path} className="contents">
              <dt className="text-xs text-theme-text-secondary">{f.label}</dt>
              <dd className={f.copied ? 'min-w-0 break-words text-theme-text-primary' : 'min-w-0 break-words text-theme-text-tertiary'}>
                {f.value} <span className="font-mono text-[11px] text-theme-text-tertiary">{f.path}</span>
              </dd>
            </div>
          ))}
        </dl>
      </div>
    </ActionConfirmDialog>
  )
}
