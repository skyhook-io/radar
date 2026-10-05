import { useState } from 'react'
import { ActionConfirmDialog, CNPGSchedulePreviewFacts, FactGrid, Tooltip, useDebouncedValue } from '@skyhook-io/k8s-ui'
import { useCNPGAction, useCNPGScheduleCapabilities, useCNPGSchedulePreview, type CNPGScheduleActionName } from '../../../api/cnpg'
import { actionOutcomeLocked, capabilityReason } from '../../../api/actions'
import { useToast } from '../../ui/Toast'
import { useCNPGWriteGuard } from './useCNPGWriteGuard'
import { cnpgOperatorActionNote } from '../operatorStatus'
import { trackCNPGOperation } from '../operations/store'

const BUTTON =
  'btn-secondary inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-2.5 py-1.5 text-xs font-medium disabled:cursor-not-allowed'

/** Suspend, resume, run a ScheduledBackup's settings once, or change its schedule. */
export function CNPGScheduleActions({ namespace, name }: { namespace: string; name: string }) {
  const caps = useCNPGScheduleCapabilities(namespace, name)
  const [open, setOpen] = useState<CNPGScheduleActionName | null>(null)
  const data = caps.data
  if (!data) return null
  const btn = (id: CNPGScheduleActionName, label: string) => {
    const cap = data.actions[id]
    return (
      <Tooltip key={id} content={capabilityReason(cap)} position="bottom">
        <button type="button" className={BUTTON} disabled={!cap.allowed} onClick={() => setOpen(id)}>
          {label}
        </button>
      </Tooltip>
    )
  }
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {btn('run', 'Run now')}
      {!data.actions.run.allowed && <span className="text-xs text-theme-text-secondary">{capabilityReason(data.actions.run)}</span>}
      {data.facts.suspended ? btn('resume', 'Resume') : btn('suspend', 'Suspend')}
      {btn('setSchedule', 'Edit schedule')}
      {open === 'setSchedule' && <EditScheduleDialog namespace={namespace} name={name} onClose={() => setOpen(null)} />}
      {open && open !== 'setSchedule' && <ScheduleDialog kind={open} namespace={namespace} name={name} onClose={() => setOpen(null)} />}
    </div>
  )
}

function EditScheduleDialog({ namespace, name, onClose }: { namespace: string; name: string; onClose: () => void }) {
  const caps = useCNPGScheduleCapabilities(namespace, name)
  const mutation = useCNPGAction('scheduledbackups', namespace, name)
  const { showSuccess } = useToast()
  const data = caps.data!
  const current = data.facts.schedule
  const [draft, setDraft] = useState(current)
  const debounced = useDebouncedValue(draft.trim(), 300)
  const preview = useCNPGSchedulePreview(namespace, name, debounced, debounced !== '')
  const guard = useCNPGWriteGuard({ namespace, name, scope: { kind: 'spec', paths: ['spec.schedule'] }, targetKind: 'ScheduledBackup' })
  const operatorNote = cnpgOperatorActionNote(data.operator)
  const next = draft.trim()
  const settled = debounced === next && preview.data?.schedule === next && !preview.isFetching
  const p = settled ? preview.data : undefined
  const disabledReason = !data.actions.setSchedule.allowed ? data.actions.setSchedule.reason : preview.error ? 'The schedule could not be checked' : undefined
  const incompleteReason =
    next === ''
      ? 'Enter a schedule'
      : next === current
        ? 'The schedule is unchanged'
        : !settled
          ? 'Checking the schedule…'
          : p && !p.valid
            ? 'The operator cannot run this schedule'
            : undefined
  const warnings = [...(operatorNote?.tone === 'warning' ? [operatorNote.text] : []), ...(p?.valid && p.runsImmediately ? ['Saving makes the operator create one backup right away: a time on the new schedule has passed since its last check.'] : [])]
  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() =>
        mutation.mutate(
          {
            action: 'setSchedule',
            request: { reviewedContext: data.context, uid: data.uid, facts: data.facts as unknown as Record<string, unknown>, params: { schedule: next } },
          },
          {
            onSuccess: () => {
              showSuccess(`${name} now runs on ${next}.`)
              onClose()
            },
          },
        )
      }
      title={`Edit ${name}'s schedule`}
      subject={{ kind: 'ScheduledBackup', namespace, name }}
      context={data.context}
      effect={`Backups of ${data.facts.cluster} are taken on the new schedule. Method, target and other settings are unchanged.`}
      notes={operatorNote?.tone === 'info' ? [operatorNote.text] : []}
      warnings={warnings}
      writes={[{ summary: `patch ScheduledBackup ${namespace}/${name}`, detail: `spec.schedule = "${next}" (was "${current}")` }]}
      guard={guard.node}
      guardSatisfied={guard.satisfied}
      confirmLabel="Save schedule"
      isLoading={mutation.isPending}
      error={mutation.error?.message}
      outcomeUnknown={actionOutcomeLocked(mutation.error)}
      disabledReason={disabledReason}
      incompleteReason={incompleteReason}
    >
      <div className="space-y-2">
        <label className="block text-xs font-medium text-theme-text-secondary" htmlFor="cnpg-schedule-input">
          Schedule · six fields, seconds first (second minute hour day-of-month month day-of-week), or a descriptor such as @daily
        </label>
        <input
          id="cnpg-schedule-input"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          spellCheck={false}
          autoComplete="off"
          className="w-full rounded-md border border-theme-border bg-theme-elevated px-2.5 py-1.5 font-mono text-sm text-theme-text-primary focus:border-accent focus:outline-none"
        />
        {preview.error && next !== '' && (
          <div className="text-xs text-theme-text-tertiary">The schedule could not be checked: {preview.error instanceof Error ? preview.error.message : 'unknown error'}</div>
        )}
        {preview.data && next !== '' && (
          <div className={settled ? undefined : 'opacity-60'}>
            <FactGrid>
              <CNPGSchedulePreviewFacts preview={preview.data} />
            </FactGrid>
          </div>
        )}
        <div className="text-[11.5px] text-theme-text-tertiary">Checked by Radar with the parser CloudNativePG uses; the operator's admission webhook checks it again on save.</div>
      </div>
    </ActionConfirmDialog>
  )
}

function ScheduleDialog({ kind, namespace, name, onClose }: { kind: CNPGScheduleActionName; namespace: string; name: string; onClose: () => void }) {
  const caps = useCNPGScheduleCapabilities(namespace, name)
  const mutation = useCNPGAction('scheduledbackups', namespace, name)
  const { showSuccess } = useToast()
  const data = caps.data!
  const overdue = !!data.facts.nextScheduleTime && Date.parse(data.facts.nextScheduleTime) < Date.now()
  const spec =
    kind === 'run'
      ? {
          title: `Create a backup using ${name}'s settings?`,
          confirm: 'Create Backup',
          effect: `Creates one Backup of ${data.facts.cluster} with this schedule's method, plugin, online and target settings. The schedule itself is not changed and its next run still happens on time.`,
          writes: [{ summary: `create Backup ${namespace}/${name}-manual-<timestamp>`, detail: 'spec copied from the ScheduledBackup (method, pluginConfiguration, online, onlineConfiguration, target)' }],
          notes: ['This Backup is not owned by the schedule, so it outlives it.'],
          scope: { kind: 'create-child' as const },
        }
      : kind === 'suspend'
        ? {
            title: `Suspend ${name}?`,
            confirm: 'Suspend',
            effect: `No new backups of ${data.facts.cluster} are taken on this schedule until it is resumed.`,
            writes: [{ summary: `patch ScheduledBackup ${namespace}/${name}`, detail: 'spec.suspend = true' }],
            notes: [] as string[],
            scope: { kind: 'spec' as const, paths: ['spec.suspend'] },
          }
        : {
            title: `Resume ${name}?`,
            confirm: 'Resume',
            effect: `Backups of ${data.facts.cluster} are taken on this schedule again.`,
            writes: [{ summary: `patch ScheduledBackup ${namespace}/${name}`, detail: 'spec.suspend = false' }],
            notes: overdue ? ['The next run is overdue, so the operator takes one catch-up backup right away. Missed runs are not replayed.'] : [],
            scope: { kind: 'spec' as const, paths: ['spec.suspend'] },
          }
  const guard = useCNPGWriteGuard({ namespace, name, scope: spec.scope, targetKind: 'ScheduledBackup' })
  const operatorNote = cnpgOperatorActionNote(data.operator)
  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() =>
        mutation.mutate(
          { action: kind, request: { reviewedContext: data.context, uid: data.uid, facts: data.facts as unknown as Record<string, unknown> } },
          {
            onSuccess: (r) => {
              if (kind === 'run' && r.backup) {
                trackCNPGOperation({
                  kind: 'run',
                  label: `Backup ${r.backup} (from ${name})`,
                  context: data.context,
                  namespace,
                  cluster: data.facts.cluster,
                  target: { name: r.backup },
                  link: { kind: 'Backup', group: 'postgresql.cnpg.io', name: r.backup },
                })
              }
              showSuccess(kind === 'run' ? `Backup ${r.backup ?? ''} requested.` : kind === 'suspend' ? `${name} suspended.` : `${name} resumed.`)
              onClose()
            },
          },
        )
      }
      title={spec.title}
      subject={{ kind: 'ScheduledBackup', namespace, name }}
      context={data.context}
      effect={spec.effect}
      notes={operatorNote?.tone === 'info' ? [...spec.notes, operatorNote.text] : spec.notes}
      warnings={operatorNote?.tone === 'warning' ? [operatorNote.text] : undefined}
      writes={spec.writes}
      guard={guard.node}
      guardSatisfied={guard.satisfied}
      confirmLabel={spec.confirm}
      isLoading={mutation.isPending}
      error={mutation.error?.message}
      outcomeUnknown={actionOutcomeLocked(mutation.error)}
      disabledReason={data.actions[kind].allowed ? undefined : data.actions[kind].reason}
    />
  )
}
