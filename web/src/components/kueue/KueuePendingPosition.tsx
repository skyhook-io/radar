import { KueuePendingPositionValue } from '@skyhook-io/k8s-ui/components/resources/KueuePendingPosition'
import { getRadarUpgradeRequirement } from '@skyhook-io/k8s-ui'
import { useKueuePendingPosition } from '../../api/client'

interface Props {
  namespace: string
  name: string
  uid?: string
  /** Cache invalidation input; the endpoint reads the current spec.queueName. */
  queueName?: string
  requeueAt?: string
}

/**
 * Kueue's reported position for a Workload eligible for a visibility lookup.
 * Callers render it only for such a Workload; an answer that belongs
 * to another incarnation of the name, or that the server judged ineligible,
 * is never shown.
 */
export function KueuePendingPosition({ namespace, name, uid, queueName, requeueAt }: Props) {
  const query = useKueuePendingPosition(namespace, name, uid, queueName, true)
  const current = query.data?.uid === uid && query.data?.eligible ? query.data : undefined
  const upgrade = getRadarUpgradeRequirement(query.error) ?? undefined
  const ineligible = query.data?.uid === uid && query.data?.eligible === false
  if (ineligible) return <span className="text-theme-text-tertiary">No pending position to look up for the current Workload state.</span>
  return <KueuePendingPositionValue
    position={current?.position}
    loading={query.isPending}
    error={current ? undefined : query.error?.message}
    refreshFailed={Boolean(current && query.isRefetchError)}
    upgrade={upgrade}
    onRetry={() => { void query.refetch() }}
    requeueAt={requeueAt}
  />
}
