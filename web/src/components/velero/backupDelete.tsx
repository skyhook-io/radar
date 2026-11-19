import { AlertBanner, type DeletePresentation, type VeleroWorkspaceResponse } from '@skyhook-io/k8s-ui'
import type { RadarFeatureSupport } from '../../api/radarFeatures'
import { useRadarFeature } from '../../api/client'
import { useVeleroWorkspace } from '../../api/velero'
import { isVeleroBackupKind } from './routes'
import { VELERO_DELETE_OBJECT } from './shared'

/**
 * How a Velero Backup is deleted from Radar:
 * - `request`: Delete backup, a DeleteBackupRequest, as `velero backup delete`
 *   does. Velero removes the backup's data and then the object.
 * - `object`: Delete object, a DELETE of the Backup object, offered only where
 *   Velero would never act on a request (`reason` says why).
 * - `pending`: not decided yet (capabilities still loading); neither is offered.
 */
export type VeleroBackupDeletePath =
  | { kind: 'request' }
  | { kind: 'object'; reason: VeleroObjectDeleteReason }
  | { kind: 'pending' }

/**
 * - `unstored`: Velero never started it (phase empty or New) and it names no
 *   storage location, so nothing of it was stored, and Velero refuses a
 *   request for a backup without a location.
 * - `rejectedNoLocation`: Velero rejected it before it ran, and the location
 *   it names does not exist (or it names none), so nothing was stored and
 *   Velero refuses the request.
 * - `olderRadar`: the connected Radar cannot create a DeleteBackupRequest.
 */
export type VeleroObjectDeleteReason = 'unstored' | 'rejectedNoLocation' | 'olderRadar'

/**
 * The delete path for one Backup. `locationMissing` says a named storage
 * location is known not to exist (the locations were read in full); undefined
 * when that can't be said.
 */
export function veleroBackupDeletePath(
  backup: any,
  deleteBackupSupport: RadarFeatureSupport,
  locationMissing: (name: string) => boolean | undefined,
): VeleroBackupDeletePath {
  // An unloaded object reads as phase "" with no location; nothing is decided from that.
  if (!backup) return { kind: 'pending' }
  if (deleteBackupSupport === 'unsupported') return { kind: 'object', reason: 'olderRadar' }
  if (deleteBackupSupport !== 'supported') return { kind: 'pending' }
  const phase: string = backup?.status?.phase ?? ''
  const location: string = backup?.spec?.storageLocation ?? ''
  if ((phase === '' || phase === 'New') && !location) return { kind: 'object', reason: 'unstored' }
  if (phase === 'FailedValidation' && (!location || locationMissing(location) === true)) return { kind: 'object', reason: 'rejectedNoLocation' }
  return { kind: 'request' }
}

/**
 * Whether the generic Delete (a DELETE of the object) is offered: for every
 * resource but a Velero Backup, and for a Backup only where its path is
 * `object`. Bulk delete stays off for Velero Backups.
 */
export function genericDeleteOffered(kind: string | undefined, group: string | undefined, path: VeleroBackupDeletePath): boolean {
  return !isVeleroBackupKind(kind, group) || path.kind === 'object'
}

/** Whether a storage location is known not to exist: the workspace read every location and it is not among them. */
export function veleroLocationMissing(workspace: VeleroWorkspaceResponse | undefined, namespace: string, name: string): boolean | undefined {
  if (!workspace) return undefined
  if (workspace.locations.some((l) => l.namespace === namespace && l.name === name)) return false
  return workspace.coverage.backupStorageLocations?.state === 'full' ? true : undefined
}

/**
 * The delete path for a resource on screen; `request` for anything that isn't
 * a Velero Backup. The workspace is read only for a Velero Backup, and shares
 * the page's request.
 */
export function useVeleroBackupDeletePath(kind: string | undefined, group: string | undefined, backup: any): VeleroBackupDeletePath {
  const isBackup = isVeleroBackupKind(kind, group)
  const { support } = useRadarFeature('veleroDeleteBackup')
  const workspaceSupport = useRadarFeature('veleroWorkspace').support
  const workspace = useVeleroWorkspace({ enabled: isBackup && workspaceSupport === 'supported' })
  if (!isBackup) return { kind: 'request' }
  const namespace: string = backup?.metadata?.namespace ?? ''
  return veleroBackupDeletePath(backup, support, (name) => veleroLocationMissing(workspace.data, namespace, name))
}

/**
 * How the header presents Delete object for a Velero Backup: a labelled
 * button, one consequence line, the reason it isn't Delete backup, and no
 * Force delete (these Backups carry no finalizer worth stripping).
 */
export function veleroObjectDeletePresentation(reason: VeleroObjectDeleteReason, backup: any): DeletePresentation {
  return {
    label: VELERO_DELETE_OBJECT,
    details:
      reason === 'olderRadar'
        ? 'Removes only the Backup object. Any data it stored stays in object storage, and Velero’s backup sync can bring the object back.'
        : 'Deletes the Backup object. Nothing of it is in storage.',
    notice: <VeleroObjectDeleteNotice reason={reason} backup={backup} />,
    allowForce: false,
  }
}

/** Why this is Delete object and not Delete backup, said first in the confirmation. */
export function VeleroObjectDeleteNotice({ reason, backup }: { reason: VeleroObjectDeleteReason; backup: any }) {
  if (reason === 'olderRadar') {
    return (
      <AlertBanner
        variant="warning"
        title="This Radar can’t ask Velero to delete a backup"
        message="To delete the backup and its data, use velero backup delete."
        className="mb-0"
      />
    )
  }
  const location: string = backup?.spec?.storageLocation ?? ''
  const schedule: string | undefined = backup?.metadata?.labels?.['velero.io/schedule-name']
  const why =
    reason === 'unstored'
      ? 'Velero never started this backup and gave it no storage location, so it won’t act on a deletion request for it.'
      : `Velero rejected this backup before it ran, and ${location ? `its storage location ${location} doesn’t exist` : 'it has no storage location'}, so it won’t act on a deletion request for it.`
  // A queued run holds its schedule: Velero submits nothing new while it is New.
  const next = reason === 'unstored' && schedule ? ` Once it’s gone, schedule ${schedule} submits its next run.` : ''
  return <AlertBanner variant="info" title="Nothing of this backup is in storage" message={`${why}${next}`} className="mb-0" />
}
