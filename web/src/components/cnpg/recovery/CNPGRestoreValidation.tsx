import { useMemo, useState } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { ActionConfirmDialog, Badge, formatAge, isApiGroup } from '@skyhook-io/k8s-ui'
import { useCNPGClusterCapabilities, useCNPGWorkspace } from '../../../api/cnpg'
import { useRecordCNPGRestoreValidation, type CNPGRecoveryResponse } from '../../../api/cnpg-recovery'
import { useCNPGWriteGuard } from '../actions/useCNPGWriteGuard'
import { useRestoreObservation, describeRecoverySource } from './CNPGRestoreProgress'
import { RESTORE_VALIDATION_ANNOTATION, formatLocal, formatUTC, sourceClusterFor, targetIsoFrom } from './restoreModel'

const CHECKLIST = [
  'Connect with psql and confirm the expected databases and roles exist',
  'Compare row counts of the tables you care about with the source',
  'Check the newest transaction timestamp against the recovery target',
  'Run the application’s own smoke query or health check against it',
]

function useSourceCluster(namespace: string, snapshot: CNPGRecoveryResponse | undefined) {
  const workspace = useCNPGWorkspace([namespace], { enabled: !!snapshot?.recovery })
  return useMemo(() => {
    const rec = snapshot?.recovery
    const objects = workspace.data?.objects
    if (!rec || !objects) return null
    const clusters = (objects.clusters ?? []).filter((c: any) => isApiGroup(c.apiVersion, 'postgresql.cnpg.io'))
    if (rec.sourceKind === 'backup') {
      const b = (objects.backups ?? []).find((x: any) => x.metadata?.namespace === namespace && x.metadata?.name === rec.backup)
      const name = b?.spec?.cluster?.name
      return name ? { namespace, name } : null
    }
    if ((rec.sourceKind === 'objectStore' && rec.objectStore && rec.serverName) || (rec.sourceKind === 'barmanObjectStore' && rec.serverName)) {
      const c =
        rec.sourceKind === 'objectStore'
          ? sourceClusterFor({ kind: 'objectStore', objectStore: rec.objectStore!, serverName: rec.serverName! }, clusters, namespace)
          : sourceClusterFor({ kind: 'inTree', barmanObjectStore: {}, serverName: rec.serverName! }, clusters, namespace)
      return c ? { namespace, name: c.metadata?.name as string } : null
    }
    return null
  }, [snapshot, workspace.data, namespace])
}

/**
 * Protection → Restore validation on a Cluster bootstrapped from backups. The
 * record is a person's note of what they checked, stored on this Cluster as
 * an annotation; Radar never turns it into a pass.
 */
export function CNPGRestoreValidation({ namespace, name }: { namespace: string; name: string }) {
  const { observation, snapshot } = useRestoreObservation(namespace, name)
  const source = useSourceCluster(namespace, snapshot)
  const [searchParams, setSearchParams] = useSearchParams()
  const location = useLocation()
  // ?validate=1 is the restore checklist's "Record what you checked" link.
  const [open, setOpenState] = useState(() => searchParams.get('validate') === '1')
  const setOpen = (next: boolean) => {
    setOpenState(next)
    if (!next && searchParams.has('validate')) {
      const params = new URLSearchParams(searchParams)
      params.delete('validate')
      setSearchParams(params, { replace: true, state: location.state })
    }
  }
  if (!snapshot?.recovery) return null
  const note = snapshot.validation
  const restoreDone = observation?.state === 'completed'

  return (
    <section className="mx-5 mt-3 overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm xl:mx-7">
      <div className="flex flex-wrap items-center gap-2 border-b border-theme-border px-4 py-2.5">
        <h2 className="text-sm font-semibold text-theme-text-primary">Restore validation</h2>
        {note ? <Badge severity="neutral" size="sm">Recorded</Badge> : <Badge severity="neutral" size="sm">Not recorded</Badge>}
        <span className="text-xs text-theme-text-tertiary">This cluster was restored from {describeRecoverySource(snapshot.recovery)}</span>
        <div className="ml-auto">
          <button type="button" onClick={() => setOpen(true)} className="btn-brand px-3 py-1.5 text-xs font-medium">
            {note ? 'Record a new note…' : 'Record validation…'}
          </button>
        </div>
      </div>
      <div className="px-4 py-3 text-sm">
        {note ? (
          <dl className="grid grid-cols-[9rem_minmax(0,1fr)] gap-x-4 gap-y-1.5">
            <dt className="text-theme-text-tertiary">Recorded</dt>
            <dd className="text-theme-text-primary">
              by {note.recordedBy || 'a user Radar could not identify (authentication is off)'} · {formatUTC(note.recordedAt)} ({formatAge(note.recordedAt)} ago)
            </dd>
            <dt className="text-theme-text-tertiary">What was checked</dt>
            <dd className="whitespace-pre-wrap break-words text-theme-text-primary">{note.checked}</dd>
            {note.targetTime && (
              <>
                <dt className="text-theme-text-tertiary">Recovery target</dt>
                <dd className="text-theme-text-primary">{formatUTC(note.targetTime)}</dd>
              </>
            )}
            <dt className="text-theme-text-tertiary">Source cluster</dt>
            <dd className="text-theme-text-primary">
              {note.source ? (
                <>
                  {note.source.namespace}/{note.source.name}{' '}
                  <span className="font-mono text-[11px] text-theme-text-tertiary">{note.source.uid ? `uid ${note.source.uid}` : 'UID not read'}</span>
                </>
              ) : (
                <span className="text-theme-text-tertiary">Not named</span>
              )}
            </dd>
            <dt className="text-theme-text-tertiary">Restored cluster</dt>
            <dd className="font-mono text-[11px] text-theme-text-tertiary">uid {note.target.uid}</dd>
          </dl>
        ) : (
          <p className="text-theme-text-secondary">
            {snapshot.validationError
              ? `The recorded note cannot be read: ${snapshot.validationError}.`
              : 'Nobody has recorded checking this restore. A restore that came up is one piece of evidence; what someone verified inside it is another.'}
          </p>
        )}
        <p className="mt-2 text-[11px] text-theme-text-tertiary">
          Stored on this Cluster as the {RESTORE_VALIDATION_ANNOTATION} annotation. It is a note, not a test Radar ran, so it never shows as passed.
        </p>
      </div>
      {open && (
        <RecordDialog
          namespace={namespace}
          name={name}
          source={source}
          defaultTarget={typeof snapshot.recovery.target?.targetTime === 'string' ? snapshot.recovery.target.targetTime : undefined}
          restoreDone={restoreDone}
          onClose={() => setOpen(false)}
        />
      )}
    </section>
  )
}

function toLocalInput(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toISOString().slice(0, 19)
}

function RecordDialog({
  namespace,
  name,
  source,
  defaultTarget,
  restoreDone,
  onClose,
}: {
  namespace: string
  name: string
  source: { namespace: string; name: string } | null
  defaultTarget?: string
  restoreDone: boolean
  onClose: () => void
}) {
  const caps = useCNPGClusterCapabilities(namespace, name)
  const mutation = useRecordCNPGRestoreValidation(namespace, name)
  const guard = useCNPGWriteGuard({ namespace, name, scope: { kind: 'metadata', paths: [`metadata.annotations["${RESTORE_VALIDATION_ANNOTATION}"]`] } })
  const [checked, setChecked] = useState('')
  const [target, setTarget] = useState(toLocalInput(defaultTarget))
  const targetIso = target ? targetIsoFrom(target, 'utc') : null
  // Reload is gated on the same grant (patch clusters), so its permission answers for this write.
  const patchCap = caps.data?.actions?.reload

  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() => {
        if (!caps.data) return
        mutation.mutate(
          {
            reviewedContext: caps.data.context,
            uid: caps.data.uid,
            params: { checked: checked.trim(), ...(targetIso ? { targetTime: targetIso } : {}), ...(source ? { source } : {}) },
          },
          { onSuccess: onClose },
        )
      }}
      title="Record restore validation"
      subject={{ kind: 'Cluster', namespace, name }}
      context={caps.data?.context}
      effect="Writes your note, your user name and the time onto this Cluster as an annotation. It records that you checked; it does not claim the restore passed."
      confirmLabel="Record note"
      guard={guard.node}
      guardSatisfied={guard.satisfied}
      isLoading={mutation.isPending}
      error={mutation.error instanceof Error ? mutation.error.message : null}
      writes={[{ summary: `patch Cluster ${namespace}/${name} metadata`, detail: `metadata.annotations["${RESTORE_VALIDATION_ANNOTATION}"] = {"checked": …, "recordedBy": <you>, "recordedAt": <now>, "source": …, "target": …}` }]}
      warnings={restoreDone ? [] : ['The restore has not completed yet; record what you checked once it has.']}
      disabledReason={caps.data && patchCap?.permission === 'denied' ? `Recording needs patch clusters in ${namespace}` : undefined}
      incompleteReason={
        !caps.data ? 'Loading…' : !checked.trim() ? 'Describe what you checked.' : target && !targetIso ? 'The recovery target is not a valid time.' : undefined
      }
    >
      <div className="space-y-3">
        <div>
          <div className="mb-1 text-xs text-theme-text-secondary">Things worth checking</div>
          <ul className="list-disc space-y-0.5 pl-5 text-xs text-theme-text-tertiary">
            {CHECKLIST.map((c) => <li key={c}>{c}</li>)}
          </ul>
        </div>
        <label className="block">
          <span className="mb-1 block text-xs text-theme-text-secondary">What you checked, and what you found</span>
          <textarea
            value={checked}
            onChange={(e) => setChecked(e.target.value)}
            maxLength={2000}
            rows={5}
            className="w-full rounded-lg border border-theme-border bg-theme-base px-2 py-1.5 text-sm text-theme-text-primary"
            placeholder="e.g. orders: 1,204,332 rows (source 1,204,340 at target); newest order 09:59:58 UTC"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-xs text-theme-text-secondary">Recovery target (UTC, optional)</span>
          <input type="datetime-local" step={1} value={target} onChange={(e) => setTarget(e.target.value)} className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm text-theme-text-primary" />
          {targetIso && <span className="ml-2 text-[11px] text-theme-text-tertiary">{formatLocal(targetIso)}</span>}
        </label>
        <div className="text-xs text-theme-text-tertiary">
          Source cluster: {source ? `${source.namespace}/${source.name} (its UID is read and recorded by the server)` : 'not found among live clusters; the note records none'}
        </div>
      </div>
    </ActionConfirmDialog>
  )
}
