import clsx from 'clsx'
import { VeleroScheduleStateBadge, getResourceStatus } from '@skyhook-io/k8s-ui'
import { useRadarFeature } from '../../api/client'
import { useVeleroScheduleActivity } from '../../api/velero'

/**
 * A Velero Schedule's header chip: what the schedule is doing (late, held, on
 * schedule) rather than Velero's phase, which only says Velero accepted it. A
 * schedule can be Enabled and hours late. Until the reading arrives, or on a
 * Radar without it, the phase stands in.
 */
export function VeleroScheduleStatusBadge({ resource }: { resource: any }) {
  const namespace = resource?.metadata?.namespace ?? ''
  const name = resource?.metadata?.name ?? ''
  const { support } = useRadarFeature('veleroWorkspace')
  const activity = useVeleroScheduleActivity(namespace, name, { enabled: support === 'supported' })
  const reading = activity.data?.reading
  if (reading) return <VeleroScheduleStateBadge reading={reading} />
  const phase = getResourceStatus('schedules', resource)
  return phase ? <span className={clsx('badge', phase.color)}>{phase.text}</span> : null
}
