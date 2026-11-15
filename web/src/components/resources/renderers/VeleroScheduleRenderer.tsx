import { VeleroScheduleActivity, type ResourceRef } from '@skyhook-io/k8s-ui'
import { VeleroScheduleRenderer as BaseVeleroScheduleRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/VeleroScheduleRenderer'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { useRadarFeature } from '../../../api/client'
import { useVeleroScheduleActivity } from '../../../api/velero'
import { RefreshFailedNotice } from '../../workspace/layout'

/**
 * Host wrapper adding "Schedule activity" to a Velero Schedule: the server's
 * reading of it against the controller's rules and its recent runs. A Radar
 * that predates the reading shows the package renderer alone.
 */
export function VeleroScheduleRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  const { support } = useRadarFeature('veleroWorkspace')
  const activity = useVeleroScheduleActivity(namespace, name, { enabled: support === 'supported' })
  const reading = activity.data?.reading
  return (
    <>
      <BaseVeleroScheduleRenderer data={data} onNavigate={onNavigate} />
      {support === 'supported' && reading && (
        <>
          {/* A failed refresh keeps the last reading; say how old it is rather than present it as current. */}
          <RefreshFailedNotice queries={[activity]} className="px-4 pb-2" />
          <VeleroScheduleActivity reading={reading} created={data?.metadata?.creationTimestamp} onNavigate={onNavigate} />
        </>
      )}
      {support === 'supported' && !reading && activity.error && (
        <LookupFailureNote errors={[activity.error]} what="this schedule’s activity" />
      )}
    </>
  )
}
