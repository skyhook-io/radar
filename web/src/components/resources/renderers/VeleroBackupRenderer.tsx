import { VeleroBackupRenderer as BaseVeleroBackupRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/VeleroBackupRenderer'
import { getBackupStorageLocation } from '@skyhook-io/k8s-ui/components/resources/resource-utils-velero'
import { useOperationalIssuesShown, type ResourceRef } from '@skyhook-io/k8s-ui'
import { useVeleroRunMessagesFetch, veleroMessagesInIssue } from '../../velero/VeleroRunMessagesHost'
import { useResources } from '../../../api/client'

/**
 * Host wrapper adding the two things a Backup cannot answer from its own status:
 * whether the storage location holding it is reachable, and what the errors and
 * warnings it counted actually say.
 *
 * The backup's own status says Completed and goes on saying it after the bucket
 * behind it goes Unavailable. This is the page someone opens to decide whether
 * they can restore to this point, so the answer belongs here and not one screen
 * away.
 *
 * Namespace is explicit — storage locations live alongside the backups that name
 * them, in Velero's own namespace. Omitting it would inherit the reader's
 * namespace view filter, which is a browsing preference and not the scope of
 * this question.
 */
export function VeleroBackupRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const messages = useVeleroRunMessagesFetch('backups', data)
  // A failed run's messages sit with its issue, which says what is wrong.
  const inIssue = veleroMessagesInIssue(data, useOperationalIssuesShown())
  const namespace = data?.metadata?.namespace ?? ''

  // An unset spec.storageLocation means Velero has not processed the backup
  // yet; there is no location to look up until it does.
  const name = getBackupStorageLocation(data)
  const locations = useResources<any>('backupstoragelocations', namespace, 'velero.io', {
    enabled: !!namespace && !!name,
  })

  // A location we have not read is not a healthy one, so nothing is claimed
  // until the lookup answers.
  const answered = !!name && !locations.isLoading && !locations.error && locations.data !== undefined
  const match = answered ? (locations.data ?? []).find((l: any) => l?.metadata?.name === name) : undefined
  const phase = match?.status?.phase

  // The location the backup names is gone. Velero restores from the location
  // recorded on the backup, so this is not restorable — and it is invisible
  // otherwise, because a location that does not exist has no phase to report.
  const missing = answered && match === undefined

  return (
    <BaseVeleroBackupRenderer
      data={data}
      storageLocationPhase={phase}
      storageLocationMissing={missing}
      messages={inIssue ? undefined : messages}
      onNavigate={onNavigate}
    />
  )
}
