import { VeleroRestoreRenderer as BaseVeleroRestoreRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/VeleroRestoreRenderer'
import { useOperationalIssuesShown, type ResourceRef } from '@skyhook-io/k8s-ui'
import { useVeleroRunMessagesFetch, veleroMessagesInIssue } from '../../velero/VeleroRunMessagesHost'

/**
 * Host wrapper adding the messages behind the run's error and warning counts.
 *
 * The counts are on the Restore; the text is not. It lives in a results file in
 * object storage that only Velero's controller can hand out a link to — so the
 * page showed a number an operator could worry about but not act on. A stuck
 * restore is the more urgent of the two kinds, because someone is waiting on
 * their data.
 *
 * Fetched on click, not on open: it creates a DownloadRequest and pulls an
 * object out of storage. Same bargain as the network trace's probes.
 */
export function VeleroRestoreRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const messages = useVeleroRunMessagesFetch('restores', data)
  // A failed run's messages sit with its issue, which says what is wrong.
  const inIssue = veleroMessagesInIssue(data, useOperationalIssuesShown())
  return <BaseVeleroRestoreRenderer data={data} onNavigate={onNavigate} messages={inIssue ? undefined : messages} />
}
