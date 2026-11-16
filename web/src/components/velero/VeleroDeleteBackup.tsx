import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Loader2, Trash2 } from 'lucide-react'
import { ActionConfirmDialog, Badge, Tooltip } from '@skyhook-io/k8s-ui'
import { showApiError } from '../ui/Toast'
import { useRadarFeature } from '../../api/client'
import { getApiBase } from '../../api/config'
import { actionDialogResult } from '../../api/actions'
import { useVeleroBackupDeletion, useVeleroDeleteBackup, useVeleroWorkspace, type VeleroDeletion } from '../../api/velero'

// The generic Delete's hover treatment, on a labelled button like the other header actions.
const BUTTON =
  'btn-secondary inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-2.5 py-1.5 text-xs font-medium hover:text-red-400 hover:border-red-400/50 hover:bg-red-500/10 disabled:cursor-not-allowed disabled:opacity-50'

// The phases Velero's backup tracker holds a backup in (pkg/velero.BackupStillRunning).
const RUNNING = new Set(['InProgress', 'WaitingForPluginOperations', 'WaitingForPluginOperationsPartiallyFailed', 'Finalizing', 'FinalizingPartiallyFailed'])
const SETTLED = new Set<VeleroDeletion['state']>(['refused', 'failed', 'unknown', 'deleted'])

/** Why Velero would refuse to delete this backup now, from what Radar can see. */
export function deleteRefusal(backup: any, location: { accessMode?: string; phase?: string } | undefined, serverStopped: boolean): string | undefined {
  const phase: string = backup?.status?.phase ?? ''
  if (RUNNING.has(phase)) return `This backup is still running (${phase}). Velero deletes a backup only once it has finished.`
  if (serverStopped) return 'The Velero server isn’t running, so nothing would be deleted now. Start it first.'
  const name: string = backup?.spec?.storageLocation ?? ''
  if (!name) return 'This backup has no storage location yet; wait for Velero to start it before deleting.'
  if (location?.accessMode === 'ReadOnly') return `Its storage location ${name} is read-only, and Velero does not delete backups from a read-only location.`
  if (location && location.phase !== 'Available') return `Its storage location ${name} is not available, and Velero deletes a backup only from an available location.`
  return undefined
}

const PARTLY = 'Some of its data may already have been removed.'

function stateText(d: VeleroDeletion): string {
  switch (d.state) {
    case 'waiting':
      return d.serverStopped ? 'Waiting for the Velero server, which isn’t running.' : 'Waiting for Velero to start deleting.'
    case 'inProgress':
      return 'Velero is deleting the backup.'
    case 'refused':
      return `Velero refused to delete the backup, and nothing was deleted: ${(d.errors ?? []).join('; ')}`
    case 'failed':
      return `Velero’s deletion failed. ${PARTLY} Velero reported: ${(d.errors ?? []).join('; ')}`
    case 'unknown':
      return 'Velero no longer has the request and the backup is still here, so how the deletion ended can’t be told. Check the backup, or delete it again.'
    case 'deleted':
      return 'Deleted.'
  }
}

/**
 * "Delete backup…" for a Velero Backup, in the drawer's header. Velero deletes
 * a backup through a DeleteBackupRequest: its files in the bucket, its volume
 * snapshots and uploaded data, and the restores made from it. Deleting the
 * Backup object instead removes none of that, and Velero recreates the object
 * from the bucket, so the generic Delete is not offered for these.
 */
export function VeleroDeleteBackup({ backup }: { backup: any }) {
  const { support } = useRadarFeature('veleroDeleteBackup')
  const workspaceSupport = useRadarFeature('veleroWorkspace').support
  const namespace: string = backup?.metadata?.namespace ?? ''
  const name: string = backup?.metadata?.name ?? ''
  const uid: string = backup?.metadata?.uid ?? ''
  const queryClient = useQueryClient()
  const workspace = useVeleroWorkspace({ enabled: support === 'supported' && workspaceSupport === 'supported' })
  const [reviewing, setReviewing] = useState<string | null>(null)
  const deleteBackup = useVeleroDeleteBackup()
  const deletion = useVeleroBackupDeletion(namespace, name, uid, support === 'supported')
  const request = deletion.data?.request ?? deleteBackup.data?.request
  const started = !!request || backup?.status?.phase === 'Deleting'

  const state = deletion.data?.state
  useEffect(() => {
    if (!request || (state !== 'failed' && state !== 'refused')) return
    const key = ['velero', 'deletion-announced', getApiBase(), namespace, request]
    if (queryClient.getQueryData(key)) return
    queryClient.setQueryDefaults(key, { gcTime: Infinity })
    queryClient.setQueryData(key, true)
    if (state === 'refused') showApiError(`Velero refused to delete backup ${name}`, (deletion.data?.errors ?? []).join('; '))
    if (state === 'failed') showApiError(`Deleting backup ${name} failed`, `${PARTLY} ${(deletion.data?.errors ?? []).join('; ')}`)
  }, [queryClient, request, state, name, namespace, deletion.data?.errors])

  if (support !== 'supported' || !namespace || !name || !uid) return null

  const locationName: string = backup?.spec?.storageLocation ?? ''
  const location = workspace.data?.locations.find((l) => l.namespace === namespace && l.name === locationName)
  const installation = workspace.data?.installations.find((i) => i.namespace === namespace)
  const serverStopped = installation?.server.replicas === 0
  const unavailable = deleteRefusal(backup, location, serverStopped)

  if (state === 'deleted') {
    return null
  }

  // Asked, and not settled: follow it. A status Radar can't read is said as
  // such; it is never a reason to offer the deletion again.
  if ((deletion.error && !deletion.data) || (started && !(state && SETTLED.has(state)))) {
    const unreadable = deletion.error && !deletion.data
    const tooltip = unreadable
      ? started
        ? `Velero was asked to delete this backup, but Radar can’t read how it is going: ${deletion.error.message}`
        : `Radar can’t read this backup’s deletion requests: ${deletion.error.message}`
      : deletion.data
        ? stateText(deletion.data)
        : 'Velero was asked to delete this backup.'
    return (
      <Tooltip content={tooltip} position="bottom">
        <span>
          {unreadable ? (
            <Badge severity="warning">{started ? 'Deletion requested' : 'Deletion status unreadable'}</Badge>
          ) : (
            <Badge severity="neutral">
              <Loader2 className="mr-1 inline h-3 w-3 animate-spin" aria-hidden />
              {deletion.data?.state === 'waiting' && deletion.data.serverStopped ? 'Waiting for Velero' : 'Deleting…'}
            </Badge>
          )}
        </span>
      </Tooltip>
    )
  }

  const open = () => {
    deleteBackup.reset()
    setReviewing(uid)
  }

  const button = (
    <button type="button" onClick={open} disabled={unavailable !== undefined} className={BUTTON}>
      <Trash2 className="h-3.5 w-3.5" aria-hidden />
      Delete backup
    </button>
  )

  return (
    <>
      {started && deletion.data && (state === 'refused' || state === 'failed' || state === 'unknown') && (
        <Tooltip content={stateText(deletion.data)} position="bottom">
          <span>
            <Badge severity="warning">{state === 'refused' ? 'Velero refused' : state === 'failed' ? 'Deletion failed' : 'Deletion outcome unknown'}</Badge>
          </span>
        </Tooltip>
      )}
      {unavailable ? (
        <Tooltip content={unavailable} position="bottom">
          <span>{button}</span>
        </Tooltip>
      ) : (
        button
      )}
      <ActionConfirmDialog
        open={reviewing !== null}
        onClose={() => setReviewing(null)}
        onConfirm={() => {
          if (!reviewing) return
          deleteBackup.mutate(
            { namespace, name, uid: reviewing },
            {
              onSuccess: () => {
                void queryClient.invalidateQueries({ queryKey: ['velero', 'deletion'] })
                setReviewing(null)
              },
            },
          )
        }}
        title={`Delete backup ${name}?`}
        subject={{ kind: 'Backup', namespace, name }}
        effect={
          <>
            Velero deletes this backup: its files in {locationName ? `storage location ${locationName}` : 'its storage location'}, its
            volume snapshots and uploaded volume data, and the restores made from it. Then the Backup itself is removed. This can’t be
            undone.
          </>
        }
        notes={[
          'Deleting only the Backup object would leave all of this in place, and Velero would bring the object back from storage. Velero does the deletion here, as velero backup delete does.',
          ...(backup?.status?.phase === 'Deleting' ? ['Velero has marked this backup Deleting: a deletion is under way, or an earlier one failed.'] : []),
        ]}
        writes={[{ summary: `create DeleteBackupRequest in namespace ${namespace}`, detail: `spec.backupName: ${name}` }]}
        typedConfirmation={name}
        confirmLabel="Delete backup"
        disruptive
        disabledReason={reviewing && reviewing !== uid ? 'This backup was recreated under the same name while this was open. Close it and review the backup again.' : unavailable}
        isLoading={deleteBackup.isPending}
        result={actionDialogResult(deleteBackup.error, { unknown: 'Velero may already have been asked to delete this backup. Open Delete backup again to retry: it sends the same request, never a second one.' })}
      />
    </>
  )
}
