import { useState } from 'react'
import { ActionConfirmDialog, Tooltip } from '@skyhook-io/k8s-ui'
import { cnpgActionOutcomeLocked, useCNPGAction, useCNPGScheduleCapabilities, type CNPGScheduleActionName } from '../../../api/cnpg'
import { useToast } from '../../ui/Toast'
import { useCNPGWriteGuard } from './useCNPGWriteGuard'
import { cnpgOperatorActionNote } from '../operatorStatus'
import { trackCNPGOperation } from '../operations/store'

const BUTTON =
  'inline-flex items-center gap-1.5 rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-xs font-medium text-theme-text-primary hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-50'

/** Suspend, resume, or run a ScheduledBackup's settings once. */
export function CNPGScheduleActions({ namespace, name }: { namespace: string; name: string }) {
  const caps = useCNPGScheduleCapabilities(namespace, name)
  const [open, setOpen] = useState<CNPGScheduleActionName | null>(null)
  const data = caps.data
  if (!data) return null
  const btn = (id: CNPGScheduleActionName, label: string) => {
    const cap = data.actions[id]
    return (
      <Tooltip key={id} content={cap.allowed ? undefined : cap.reason ?? 'Not allowed'} position="bottom">
        <button type="button" className={BUTTON} disabled={!cap.allowed} onClick={() => setOpen(id)}>
          {label}
        </button>
      </Tooltip>
    )
  }
  return (
    <div className="flex items-center gap-1.5">
      {btn('run', 'Run now')}
      {data.facts.suspended ? btn('resume', 'Resume') : btn('suspend', 'Suspend')}
      {open && <ScheduleDialog kind={open} namespace={namespace} name={name} onClose={() => setOpen(null)} />}
    </div>
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
          { action: kind, request: { reviewedContext: data.context, uid: data.uid, facts: data.facts as unknown as Record<string, unknown> }, successMessage: '' },
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
      outcomeUnknown={cnpgActionOutcomeLocked(mutation.error)}
      disabledReason={data.actions[kind].allowed ? undefined : data.actions[kind].reason}
    />
  )
}
