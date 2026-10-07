import { useMemo, useState } from 'react'
import yaml from 'yaml'
import { ActionConfirmDialog, ConfirmDialog, FoldSection, isApiGroup } from '@skyhook-io/k8s-ui'
import { useCNPGRuntime, useCNPGWorkspace } from '../../../api/cnpg'
import { useCNPGRestoreCapability } from '../../../api/cnpg-recovery'
import { useResources } from '../../../api/client'
import { CNPGTargetFields } from '../creation/CNPGTargetFields'
import { cnpgTargetDraft, cnpgTargetFromManifest, cnpgTargetIssue, onlyCNPGTargetChanged, sameCNPGRecoveryIdentity, updateCNPGTargetYaml, withCNPGTarget, type CNPGTarget } from '../creation/targetModel'
import { useConnection } from '../../../context/ConnectionContext'
import { CreateResourceDialog } from '../../shared/CreateResourceDialog'
import { useToast } from '../../ui/Toast'
import { cnpgClusterFullPath } from '../paths'
import { trackCNPGOperation } from '../operations/store'
import { CNPG_RESTORE_OPERATION } from './restoreOperation'
import { CNPGRecoveryPoint } from './CNPGRecoveryPoint'
import {
  buildRestoreManifest,
  describeSource,
  formatUTC,
  pitrWarnings,
  preflightFacts,
  recoveryEvidenceFor,
  restoreManifestHeader,
  restoreSourceForBackup,
  assessRestoreSources,
  restoreSourcesForStore,
  sourceClusterFor,
  restorePermission,
  sourcePinsBackup,
  targetIsoFrom,
  type RestoreSource,
  type RestoreTarget,
} from './restoreModel'
import { useCNPGNavigate } from '../useCNPGNavigate'

/** Where the restore was started from; decides the default source. */
export type CNPGRestoreEntry = { kind: 'cluster'; name: string } | { kind: 'backup'; name: string } | { kind: 'objectStore'; name: string }

/**
 * Restore builds a new Cluster manifest, shows the evidence for the recovery
 * target and what the manifest copies from the source, and hands it to
 * Radar's create flow (server dry-run, strict create). After the create the
 * user is offered the new Cluster's page, which follows the restore.
 */
export function CNPGRestoreDialog({ namespace, entry, onClose }: { namespace: string; entry: CNPGRestoreEntry; onClose: () => void }) {
  const navigate = useCNPGNavigate()
  const { connection } = useConnection()
  const [context] = useState(connection.context)
  const { showSuccess } = useToast()
  const workspace = useCNPGWorkspace([namespace])
  const objects = workspace.data?.objects
  const clusters = useMemo(() => (objects?.clusters ?? []).filter((c: any) => isApiGroup(c.apiVersion, 'postgresql.cnpg.io')), [objects])
  const backups = useMemo(() => (objects?.backups ?? []).filter((b: any) => isApiGroup(b.apiVersion, 'postgresql.cnpg.io')), [objects])
  const stores = useMemo(() => objects?.objectStores ?? [], [objects])

  const sources = useMemo<RestoreSource[]>(() => {
    if (!objects) return []
    if (entry.kind === 'cluster') {
      const c = clusters.find((x: any) => x.metadata?.namespace === namespace && x.metadata?.name === entry.name)
      return assessRestoreSources(workspace.data, namespace, c).sources
    }
    if (entry.kind === 'backup') {
      const b = backups.find((x: any) => x.metadata?.namespace === namespace && x.metadata?.name === entry.name)
      const owner = clusters.find((x: any) => x.metadata?.namespace === namespace && x.metadata?.name === b?.spec?.cluster?.name) ?? null
      const s = b ? restoreSourceForBackup(b, owner) : null
      return s ? [s] : []
    }
    const store = stores.find((x: any) => x.metadata?.namespace === namespace && x.metadata?.name === entry.name)
    return store ? restoreSourcesForStore(store) : []
  }, [objects, clusters, backups, stores, entry, namespace, workspace.data])

  const [sourceIdx, setSourceIdx] = useState(0)
  const [confirmedArchive, setConfirmedArchive] = useState<string | null>(null)
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
  const archiveKey = source?.kind === 'objectStore' && source.backupName ? [sourceCluster?.metadata?.uid, source.objectStore, source.serverName, source.backupID, source.majorVersion].join('/') : null
  const archiveConfirmed = archiveKey !== null && confirmedArchive === archiveKey
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
  const [manifestBasis, setManifestBasis] = useState<string | null>(null)
  const [editingManifest, setEditingManifest] = useState(false)
  const [replacementManifest, setReplacementManifest] = useState<string | null>(null)
  const [targetOverrides, setTargetOverrides] = useState<Partial<CNPGTarget>>({})
  const [customIdentity, setCustomIdentity] = useState(false)
  const [setupStep, setSetupStep] = useState<'source' | 'target'>('source')
  const storageClasses = useResources<any>('storageclasses', undefined, 'storage.k8s.io')
  const restoreCap = useCNPGRestoreCapability(namespace)

  const evidence = useMemo(
    () => recoveryEvidenceFor(source, { sourceCluster, stores, backups, runtime: runtime.data, namespace }),
    [source, sourceCluster, stores, backups, runtime.data, namespace],
  )
  const targetIso = effectiveKind === 'time' ? targetIsoFrom(timeValue, zone) : null
  const warnings = effectiveKind === 'time' && targetIso ? pitrWarnings(targetIso, evidence) : []
  const facts = preflightFacts(sourceCluster).filter((fact) => !['Instances', 'Image', 'Data storage'].includes(fact.label))
  const serverName = source && source.kind !== 'backup' ? source.serverName : undefined
  const recoveryTarget: RestoreTarget = effectiveKind === 'time' && targetIso ? { kind: 'time', iso: targetIso } : effectiveKind === 'backupEnd' ? { kind: 'backupEnd' } : { kind: 'latest' }
  const baseManifest = source ? buildRestoreManifest({ sourceCluster, source, namespace, newName: name, target: recoveryTarget }) : { metadata: { name, namespace }, spec: { instances: 1, storage: {} } }
  const targetValues: CNPGTarget = { ...cnpgTargetFromManifest(baseManifest), ...targetOverrides, name, namespace }
  const catalog = baseManifest.spec.imageCatalogRef
  const imageDescription = catalog ? `${catalog.kind ?? 'ImageCatalog'} ${catalog.name}, PostgreSQL ${catalog.major}` : baseManifest.spec.imageName
  const requireImage = !imageDescription
  const prepareManifest = (values: CNPGTarget) => restoreManifestHeader(describeSource(source!), serverName, sourceName ?? null) + yaml.stringify(withCNPGTarget(baseManifest, values), { version: '1.1' })
  const retainingDraft = source && manifest !== null && prepareManifest(targetValues) === manifestBasis

  if (context !== connection.context) {
    return <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() => {}}
      title="Restore to a new cluster"
      subject={{ kind: 'Cluster', namespace, name }}
      context={context || undefined}
      effect="This restore was prepared in a different Kubernetes context."
      confirmLabel="Review manifest"
      disabledReason="Close this dialog and start again in the current context."
    />
  }

  if (replacementManifest !== null) {
    return <ConfirmDialog
      open
      onClose={() => setReplacementManifest(null)}
      onConfirm={() => {
        setManifest(replacementManifest)
        setManifestBasis(replacementManifest)
        setCustomIdentity(false)
        setReplacementManifest(null)
        setEditingManifest(true)
      }}
      variant="warning"
      showWarning={false}
      title="Replace edited restore manifest?"
      message="The setup changed. Rebuilding the manifest uses these setup choices and discards your YAML edits."
      confirmLabel="Replace manifest"
      cancelLabel="Keep editing setup"
    />
  }

  if (editingManifest && manifest !== null) {
    return (
      <CreateResourceDialog
        open
        onClose={onClose}
        onBack={(draft) => {
          setManifest(draft)
          const parsed = cnpgTargetDraft(draft)
          if (parsed && parsed.metadata.namespace === namespace) {
            const values = cnpgTargetFromManifest(parsed)
            setNewName(values.name)
            setTargetOverrides(values)
            setManifestBasis(prepareManifest(values))
            setCustomIdentity(!sameCNPGRecoveryIdentity(parsed, baseManifest))
          } else setCustomIdentity(true)
          setEditingManifest(false)
        }}
        backLabel="Back to restore setup"
        initialYaml={manifest}
        initialMode="create"
        lockMode
        title={`Restore into a new cluster ${name}`}
        onCreated={(created, submittedYaml) => {
          if (created.kind !== 'Cluster' || !isApiGroup(created.apiVersion, 'postgresql.cnpg.io')) return
          const submitted = cnpgTargetDraft(submittedYaml)
          const restoring = submitted?.metadata.name === created.name && submitted.metadata.namespace === created.namespace && !!submitted.spec.bootstrap?.recovery
          if (restoring) trackCNPGOperation({
            kind: CNPG_RESTORE_OPERATION,
            label: `Restore into ${created.name}`,
            context: connection.context,
            namespace: created.namespace || namespace,
            cluster: created.name,
            clusterUID: created.uid,
            baseline: { source: source && sameCNPGRecoveryIdentity(submitted, baseManifest) ? describeSource(source) : undefined },
          })
          const path = cnpgClusterFullPath(created.namespace || namespace, created.name, connection.context || undefined)
          showSuccess(`Cluster ${created.name} created`, restoring ? 'The operator is restoring it from backup.' : 'Review its current state on the Cluster page.', { label: restoring ? 'Follow the restore' : 'Open Cluster', onClick: () => navigate(path) })
        }}
      />
    )
  }

  const permission = restorePermission(namespace, restoreCap.data, restoreCap.error)
  const availability = entry.kind === 'cluster' ? assessRestoreSources(workspace.data, namespace, clusters.find((c: any) => c.metadata?.namespace === namespace && c.metadata?.name === entry.name)) : undefined
  const noSource = !workspace.isLoading && sources.length === 0
  const disabledReason =
    permission.blocked ??
    (!workspace.isLoading && sources.length === 0
      ? entry.kind === 'backup'
        ? 'This Backup cannot be restored: it has not completed, or its object store and backup ID are not recorded.'
        : entry.kind === 'objectStore'
          ? 'This ObjectStore reports no server with backups yet.'
          : availability?.unreadReason ?? availability?.disabledReason
      : undefined)
  const incompleteReason =
    permission.pending ??
    (workspace.isLoading
      ? 'Loading backups and object stores…'
      : setupStep === 'target' && !retainingDraft && cnpgTargetIssue(targetValues, requireImage)
        ? cnpgTargetIssue(targetValues, requireImage)
        : setupStep === 'target' && clusters.some((c: any) => c.metadata?.namespace === namespace && c.metadata?.name === name)
          ? `A Cluster named ${name} already exists in ${namespace}.`
          : source?.kind === 'objectStore' && source.backupName && !archiveConfirmed
            ? 'Confirm that the selected archive still contains this named backup.'
          : effectiveKind === 'time' && !targetIso
            ? 'Enter the point in time to recover to.'
            : undefined)

  return (
    <ActionConfirmDialog
      open
      size="wide"
      onClose={onClose}
      onBack={setupStep === 'target' ? () => setSetupStep('source') : undefined}
      backLabel="Back to recovery point"
      onConfirm={() => {
        if (!source) return
        if (setupStep === 'source') { setSetupStep('target'); return }
        // The apiserver reads YAML 1.1, where unquoted on/off/yes are booleans (postgresql parameters are strings).
        const nextManifest = prepareManifest(targetValues)
        const previous = manifestBasis === null ? null : cnpgTargetDraft(manifestBasis)
        const next = cnpgTargetDraft(nextManifest)
        if (manifest !== null && nextManifest !== manifestBasis && previous && next && onlyCNPGTargetChanged(previous, next, targetValues)) {
          const updated = updateCNPGTargetYaml(manifest, targetValues)
          if (updated !== null) {
            setManifest(updated)
            setManifestBasis(nextManifest)
            setEditingManifest(true)
            return
          }
        }
        if (manifest !== null && manifest !== manifestBasis && nextManifest !== manifestBasis) {
          setReplacementManifest(nextManifest)
          return
        }
        if (nextManifest !== manifestBasis) { setManifest(nextManifest); setCustomIdentity(false) }
        setManifestBasis(nextManifest)
        setEditingManifest(true)
      }}
      title="Restore to a new cluster"
      subject={{ kind: entry.kind === 'cluster' ? 'Cluster' : entry.kind === 'backup' ? 'Backup' : 'ObjectStore', namespace, name: entry.name }}
      context={connection.context || undefined}
      effect="Restore backups into a separate Cluster. The source Cluster is left unchanged."
      confirmLabel={setupStep === 'source' ? 'Continue to new Cluster' : customIdentity && retainingDraft ? 'Review current YAML' : 'Review manifest'}
      warnings={noSource ? [] : [
        ...(customIdentity ? ['Advanced YAML changed the recovery source, image or resource shape. The setup shows the original source evidence; review the current YAML for the intended configuration.'] : []),
        ...(availability?.unreadReason ? [availability.unreadReason] : []),
        ...(source?.majorVersion ? [`This backup was taken on PostgreSQL ${source.majorVersion}. Physical recovery requires that major version.`] : pinned ? ['This Backup does not record its PostgreSQL major. Choose a matching image; the current source image may have changed since the backup.'] : []),
        ...(setupStep === 'target' ? ['The new cluster has no WAL archiving or backups until you configure them.', ...(serverName ? [`If you add archiving later, do not reuse server name "${serverName}": the new cluster would write into the archive it restores from.`] : [])] : []),
        ...warnings,
        ...(permission.unchecked ? [permission.unchecked] : []),
      ]}
      disabledReason={disabledReason}
      incompleteReason={incompleteReason}
      notes={manifest !== null && manifest !== manifestBasis ? ['Your YAML edits are retained. Name, instance and storage changes preserve them; changing the recovery source or time asks before replacing them.'] : []}
    >
      {noSource ? (
        <p className="text-sm text-theme-text-secondary">
          {availability?.unreadReason ?? 'A restore needs a completed Backup, or WAL archived to a backup destination. The Backups tab shows what this cluster has and where to set it up.'}
        </p>
      ) : (
      <>
      <div className="mb-4 flex flex-wrap items-center gap-2 text-xs text-theme-text-tertiary" aria-label="Restore steps">
        <span className={setupStep === 'source' ? 'font-semibold text-accent-text' : undefined}>1. Recovery point</span><span aria-hidden>→</span>
        <span className={setupStep === 'target' ? 'font-semibold text-accent-text' : undefined}>2. New Cluster</span><span aria-hidden>→</span><span>3. Review & create</span>
      </div>
      {manifest !== null && <button type="button" className="btn-secondary mb-4 px-3 py-1.5 text-xs" onClick={() => setEditingManifest(true)}>Continue editing current YAML</button>}
      {source?.kind === 'objectStore' && source.backupName && <label className="mb-4 flex items-start gap-2 text-sm text-theme-text-secondary">
        <input type="checkbox" checked={archiveConfirmed} onChange={(event) => setConfirmedArchive(event.target.checked ? archiveKey : null)} className="mt-1" />
        <span>I confirm ObjectStore {source.objectStore}, archive server {source.serverName}, still contains backup {source.backupID}. The plugin does not record its archive destination in the Backup; this is the Cluster’s current destination.</span>
      </label>}
      {setupStep === 'source' ? <CNPGRecoveryPoint
        sources={sources}
        sourceIndex={sourceIdx}
        onSourceChange={(index) => { setSourceIdx(index); setTargetKind(null); setTargetOverrides({}); setConfirmedArchive(null) }}
        targetKind={effectiveKind}
        onTargetChange={setTargetKind}
        timeValue={timeValue}
        onTimeChange={setTimeValue}
        zone={zone}
        onZoneChange={setZone}
        targetIso={targetIso}
        evidence={evidence}
        sourceClusterName={sourceName}
      /> : <>
        <div className="mb-4 rounded-lg border border-theme-border bg-theme-base p-3 text-sm">
          <div className="font-medium text-theme-text-primary">{describeSource(source!)}</div>
          <div className="mt-1 text-theme-text-secondary">{effectiveKind === 'time' && targetIso ? `Recover to ${formatUTC(targetIso)}` : effectiveKind === 'backupEnd' ? 'Recover to the end of this backup' : 'Recover to the latest archived WAL'}</div>
        </div>
          <CNPGTargetFields value={targetValues} onChange={(values) => { setNewName(values.name); setTargetOverrides(values) }} idPrefix="cnpg-restore" namespaceFixed storageClasses={storageClasses.data?.map((item) => item.metadata.name)} imageDescription={imageDescription} imageRequired={requireImage} defaultClassDisabled={(manifest !== null ? cnpgTargetDraft(manifest) : baseManifest)?.spec.storage?.pvcTemplate?.storageClassName === ''} />
          {storageClasses.isError && <p className="text-xs text-theme-text-tertiary">Storage class suggestions could not be read. You can enter a name directly.</p>}
      <div className="mt-4">
        <FoldSection title="Other source settings" summary={sourceName ? `${sourceName} · ${facts.length} settings seed the manifest` : 'Source settings are unavailable'} attention={!sourceName}>
        <p className="mb-2 text-xs text-theme-text-tertiary">{sourceName ? 'These settings seed the manifest. Review Advanced YAML for the final configuration and any edits.' : 'Review Advanced YAML for PostgreSQL settings, resource requests and placement.'}</p>
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
        </FoldSection>
      </div>
      </>}
      </>
      )}
    </ActionConfirmDialog>
  )
}
