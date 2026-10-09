import { useState } from 'react'
import {
  ActionConfirmDialog,
  FactGrid,
  FactRow,
  RadarUpgradeNote,
  getRadarUpgradeRequirement,
} from '@skyhook-io/k8s-ui'
import { useCNPGProtectionAction, useCNPGScheduleMethodPreview } from '../../../api/cnpg-protection'
import { useConnection } from '../../../context/ConnectionContext'
import { actionOutcomeLocked } from '../../../api/actions'
import { useCNPGWriteGuard } from '../actions/useCNPGWriteGuard'
import { useToast } from '../../ui/Toast'

export function CNPGScheduleRepairButton({ namespace, name }: { namespace: string; name: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" className="btn-secondary px-3 py-1.5 text-xs" onClick={() => setOpen(true)}>
        Match {name} to the Barman plugin…
      </button>
      {open && <CNPGScheduleRepairDialog namespace={namespace} name={name} onClose={() => setOpen(false)} />}
    </>
  )
}

export function CNPGScheduleRepairDialog({
  namespace,
  name,
  onClose,
}: {
  namespace: string
  name: string
  onClose: () => void
}) {
  const preview = useCNPGScheduleMethodPreview(namespace, name)
  const mutation = useCNPGProtectionAction(namespace, name)
  const { connection } = useConnection()
  const [context] = useState(connection.context)
  const { showSuccess } = useToast()
  const guard = useCNPGWriteGuard({
    namespace,
    name,
    targetKind: 'ScheduledBackup',
    scope: { kind: 'spec', paths: ['spec.method', 'spec.pluginConfiguration.name'] },
  })
  const data = preview.data
  const changed = connection.context !== context || (data && data.context !== context)
  const upgrade = getRadarUpgradeRequirement(preview.error)
  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() => {
        if (!data) return
        mutation.mutate(
          {
            kind: 'scheduledbackups',
            action: 'repairMethod',
            request: { reviewedContext: data.context, uid: data.uid, facts: data.facts },
          },
          {
            onSuccess: () => {
              showSuccess(`${name} now uses the Cluster’s Barman plugin. Verify its next backup.`)
              onClose()
            },
          },
        )
      }}
      title="Repair backup schedule method"
      subject={{ kind: 'ScheduledBackup', namespace, name }}
      context={context}
      effect="Use the Cluster’s existing Barman plugin for this schedule. Cluster target, timing, immediate/online choices and other settings are preserved."
      disabledReason={
        changed
          ? 'The context changed. Close and review this repair again.'
          : data?.unchanged
            ? 'The schedule already matches the Barman plugin.'
            : preview.error
              ? preview.error.message
              : undefined
      }
      incompleteReason={!data && !preview.error ? 'Checking the method and server dry-run…' : undefined}
      guard={data ? guard.node : undefined}
      guardSatisfied={!!data && guard.satisfied}
      confirmLabel="Repair method"
      isLoading={mutation.isPending}
      error={mutation.error?.message}
      outcomeUnknown={actionOutcomeLocked(mutation.error)}
      writes={
        data
          ? [
              {
                summary: `patch ScheduledBackup ${namespace}/${name}`,
                detail: `spec.method = plugin; spec.pluginConfiguration.name = barman-cloud.cloudnative-pg.io`,
              },
            ]
          : undefined
      }
    >
      {upgrade ? (
        <RadarUpgradeNote requirement={upgrade} />
      ) : data ? (
        <FactGrid>
          <FactRow label="Cluster">{data.cluster}</FactRow>
          <FactRow label="Current method">{data.previousMethod}</FactRow>
          <FactRow label="After repair">plugin · barman-cloud.cloudnative-pg.io</FactRow>
        </FactGrid>
      ) : preview.error ? (
        <button
          type="button"
          onClick={() => void preview.refetch()}
          disabled={preview.isFetching}
          className="text-accent-text hover:underline"
        >
          Retry review
        </button>
      ) : null}
    </ActionConfirmDialog>
  )
}
