import { VeleroScheduleActivity, type ResourceRef } from '@skyhook-io/k8s-ui'
import { VeleroScheduleRenderer as BaseVeleroScheduleRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/VeleroScheduleRenderer'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { useRadarFeature } from '../../../api/client'
import { useVeleroScheduleActivity } from '../../../api/velero'
import { RefreshFailedNotice } from '../../workspace/layout'
import { useWorkspaceNavigate } from '../../workspace/useWorkspaceNavigate'
import { veleroDetailPath } from '../../velero/routes'

/**
 * Host wrapper leading a Velero Schedule with "Schedule activity": the server's
 * reading of it against the controller's rules and its recent runs. A Radar
 * that predates the reading shows the package renderer alone.
 */
export function VeleroScheduleRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  const { support } = useRadarFeature('veleroWorkspace')
  const activity = useVeleroScheduleActivity(namespace, name, { enabled: support === 'supported' })
  const reading = activity.data?.reading
  const navigate = useWorkspaceNavigate()
  // Every run the schedule keeps is on its page's Runs tab.
  const showAllRuns = () => navigate(veleroDetailPath({ plural: 'schedules', namespace, name }, undefined, 'runs'))
  const activitySection = support === 'supported' && reading ? (
    <>
      {/* A failed refresh keeps the last reading; say how old it is rather than present it as current. */}
      <RefreshFailedNotice queries={[activity]} className="px-4 pb-2" />
      <VeleroScheduleActivity reading={reading} created={data?.metadata?.creationTimestamp} onNavigate={onNavigate} onShowAllRuns={showAllRuns} />
    </>
  ) : support === 'supported' && activity.error ? (
    <LookupFailureNote errors={[activity.error]} what="this schedule’s activity" />
  ) : undefined
  return <BaseVeleroScheduleRenderer data={data} onNavigate={onNavigate} activity={activitySection} />
}
