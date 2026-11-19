import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Loader2, Trash2 } from 'lucide-react'
import { AlertBanner, ForceDeleteConfirmDialog, type VeleroWorkspaceResponse } from '@skyhook-io/k8s-ui'
import type { RadarFeatureSupport } from '../../api/radarFeatures'
import { useDeleteResource, useRadarFeature } from '../../api/client'
import { useVeleroBackupDeletion, useVeleroWorkspace } from '../../api/velero'
import { VELERO_BACKUP_KIND, VELERO_GROUP } from './routes'
import { VELERO_DELETE_BUTTON, VELERO_DELETE_OBJECT } from './shared'

/**
 * How a Velero Backup is deleted from Radar:
 * - `request`: Delete backup, a DeleteBackupRequest, as `velero backup delete`
 *   does. Velero removes the backup's data and then the object.
 * - `object`: Delete object, a DELETE of the Backup object, offered only where
 *   Velero would never act on a request (`reason` says why).
 * - `pending`: not decided yet; neither is offered.
 */
export type VeleroBackupDeletePath =
  | { kind: 'request' }
  | { kind: 'object'; reason: VeleroObjectDeleteReason }
  | { kind: 'pending' }

/**
 * - `unstored`: Velero never started it (phase empty or New) and it names no
 *   storage location, so nothing of it was stored, and Velero refuses a
 *   request for a backup without a location. Queued and ReadyToStart are not
 *   included: Velero's queue may be handing the backup to its controller.
 * - `rejectedNoLocation`: Velero rejected it before it ran (a rejected run
 *   writes nothing), and the location it names does not exist (or it names
 *   none), so Velero refuses the request.
 */
export type VeleroObjectDeleteReason = 'unstored' | 'rejectedNoLocation'

export interface VeleroBackupDeleteInput {
  deleteBackup: RadarFeatureSupport
  /** The connected Radar binds a DELETE to the reviewed UID and resourceVersion. */
  preconditions: RadarFeatureSupport
  /** Whether a named storage location is known not to exist; undefined when that can't be said. */
  locationMissing: (name: string) => boolean | undefined
  /** Its DeleteBackupRequests: one open (or unreadable), none open, or not read yet. */
  request: 'open' | 'none' | 'unknown'
}

/** The delete path for one Backup. */
export function veleroBackupDeletePath(backup: any, input: VeleroBackupDeleteInput): VeleroBackupDeletePath {
  // An unloaded object reads as phase "" with no location; nothing is decided from that.
  if (!backup || input.deleteBackup !== 'supported') return { kind: 'pending' }
  // Without the reviewed-object binding, Velero could start it between review
  // and delete, so a Radar without it keeps Delete backup.
  if (input.preconditions !== 'supported') return { kind: 'request' }
  if (backup.metadata?.deletionTimestamp || (backup.metadata?.finalizers ?? []).length > 0) return { kind: 'request' }
  const phase: string = backup.status?.phase ?? ''
  const location: string = backup.spec?.storageLocation ?? ''
  const reason: VeleroObjectDeleteReason | undefined =
    (phase === '' || phase === 'New') && !location
      ? 'unstored'
      : phase === 'FailedValidation' && (!location || input.locationMissing(location) === true)
        ? 'rejectedNoLocation'
        : undefined
  if (!reason) return { kind: 'request' }
  // A request already sent is followed where it is, never doubled by a second delete.
  if (input.request === 'unknown') return { kind: 'pending' }
  if (input.request === 'open') return { kind: 'request' }
  return { kind: 'object', reason }
}

/** Whether a storage location is known not to exist: the workspace read every location in its namespace and it is not among them. */
export function veleroLocationMissing(workspace: VeleroWorkspaceResponse | undefined, namespace: string, name: string): boolean | undefined {
  if (!workspace) return undefined
  if (workspace.locations.some((l) => l.namespace === namespace && l.name === name)) return false
  const cov = workspace.coverage.backupStorageLocations
  const read = cov?.state === 'full' || (cov?.state === 'partial' && !!cov.allowedNamespaces?.includes(namespace))
  return read ? true : undefined
}

/** The confirmation's consequence line, in place of the generic "This will permanently delete…". */
export const VELERO_DELETE_OBJECT_DETAILS = 'Deletes the Backup object. Nothing of it is in storage.'

/** Why this is Delete object and not Delete backup, said first in the confirmation. */
export function VeleroObjectDeleteNotice({ reason, backup }: { reason: VeleroObjectDeleteReason; backup: any }) {
  const location: string = backup?.spec?.storageLocation ?? ''
  const schedule: string | undefined = backup?.metadata?.labels?.['velero.io/schedule-name']
  const why =
    reason === 'unstored'
      ? 'Velero never started this backup and gave it no storage location, so it won’t act on a deletion request for it.'
      : `Velero rejected this backup before it ran, so it wrote nothing, and ${location ? `its storage location ${location} doesn’t exist` : 'it has no storage location'}, so Velero won’t act on a deletion request for it.`
  // Velero submits nothing new for a schedule while one of its runs is New.
  const held = reason === 'unstored' && schedule ? ` While it is queued, Velero holds schedule ${schedule}; deleting it removes that hold.` : ''
  return <AlertBanner variant="info" title="Nothing of this backup is in storage" message={`${why}${held}`} className="mb-0" />
}

/**
 * Delete object, in the Backup header where Delete backup otherwise is: the
 * generic delete (DELETE /api/resources/...) bound to the object the user
 * reviewed, so a backup Velero picked up meanwhile is refused (409) rather
 * than deleted. No Force delete: these Backups carry no finalizer to strip.
 * No dependents preview either: the only objects Velero owns by a Backup are
 * its PodVolumeBackups and DataUploads, which it creates while the backup
 * runs, and these never ran (Velero v1.18.2 podvolume/backupper.go,
 * backup/actions/csi/pvc_action.go).
 */
export function VeleroDeleteObject({ backup, reason }: { backup: any; reason: VeleroObjectDeleteReason }) {
  const namespace: string = backup?.metadata?.namespace ?? ''
  const name: string = backup?.metadata?.name ?? ''
  const [reviewed, setReviewed] = useState<{ uid: string; resourceVersion: string } | null>(null)
  const queryClient = useQueryClient()
  // Deleted, refused (it changed since review) or failed: show the backup as it is now, even if this button is gone by then.
  const deleteResource = useDeleteResource({
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ['resource'] })
      void queryClient.invalidateQueries({ queryKey: ['velero'] })
    },
  })
  return (
    <>
      <button
        type="button"
        disabled={deleteResource.isPending}
        onClick={() => setReviewed({ uid: backup?.metadata?.uid ?? '', resourceVersion: backup?.metadata?.resourceVersion ?? '' })}
        className={VELERO_DELETE_BUTTON}
      >
        {deleteResource.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden /> : <Trash2 className="h-3.5 w-3.5" aria-hidden />}
        {VELERO_DELETE_OBJECT}
      </button>
      <ForceDeleteConfirmDialog
        open={reviewed !== null}
        onClose={() => setReviewed(null)}
        onConfirm={() => {
          if (!reviewed) return
          deleteResource.mutate({
            kind: VELERO_BACKUP_KIND.name,
            group: VELERO_GROUP,
            namespace,
            name,
            uid: reviewed.uid,
            resourceVersion: reviewed.resourceVersion,
          })
          setReviewed(null)
        }}
        resourceName={name}
        resourceKind="Backup"
        namespaceName={namespace}
        resourceData={backup}
        isLoading={deleteResource.isPending}
        actionLabel={VELERO_DELETE_OBJECT}
        details={VELERO_DELETE_OBJECT_DETAILS}
        notice={<VeleroObjectDeleteNotice reason={reason} backup={backup} />}
        allowForce={false}
      />
    </>
  )
}

/**
 * Why Delete object is offered for this Backup, or undefined: the header's
 * decision for surfaces that only name the action (the page's next step). It
 * reads the same features, workspace and deletion requests, so it agrees with
 * the header once they are loaded.
 */
export function useVeleroDeleteObjectReason(backup: any): VeleroObjectDeleteReason | undefined {
  const deleteBackup = useRadarFeature('veleroDeleteBackup').support
  const preconditions = useRadarFeature('resourceDeletePreconditions').support
  const workspaceSupport = useRadarFeature('veleroWorkspace').support
  const namespace: string = backup?.metadata?.namespace ?? ''
  const workspace = useVeleroWorkspace({ enabled: !!backup && deleteBackup === 'supported' && workspaceSupport === 'supported' })
  const deletion = useVeleroBackupDeletion(namespace, backup?.metadata?.name ?? '', backup?.metadata?.uid ?? '', !!backup && deleteBackup === 'supported')
  const settled = deletion.data && ['refused', 'failed', 'unknown', 'deleted'].includes(deletion.data.state)
  const request = deletion.error ? 'open' : !deletion.data ? 'unknown' : deletion.data.request && !settled ? 'open' : 'none'
  const path = veleroBackupDeletePath(backup, {
    deleteBackup,
    preconditions,
    locationMissing: (n) => veleroLocationMissing(workspace.data, namespace, n),
    request,
  })
  return path.kind === 'object' ? path.reason : undefined
}
