import { healthColors, type StatusBadge } from '../resources/resource-utils'
import { getKueueQueueCondition, getLocalQueueStatus } from '../resources/resource-utils-kueue'
import type { RelatedRead } from './related-read'

/** The condition describes the object's current generation, as the object itself records it. */
export function conditionIsCurrent(condition: { observedGeneration?: number } | undefined, generation: number | undefined): boolean {
  return !!condition?.observedGeneration && !!generation && condition.observedGeneration === generation
}

/**
 * The LocalQueue reports ClusterQueueIsInactive and its ClusterQueue, read
 * now, reports it was stopped on purpose. Both reports must be current for
 * their own generation; anything less leaves the LocalQueue's own tone.
 */
export function localQueueInactiveBecauseParentStopped(localQueue: any, parent: RelatedRead | undefined): boolean {
  const own = getKueueQueueCondition(localQueue)
  if (own?.status !== 'False' || own.reason !== 'ClusterQueueIsInactive') return false
  if (!conditionIsCurrent(own, localQueue?.metadata?.generation)) return false
  if (parent?.state !== 'found' || parent.object?.metadata?.name !== localQueue?.spec?.clusterQueue) return false
  const theirs = getKueueQueueCondition(parent.object)
  return theirs?.status === 'False' && theirs.reason === 'Stopped' && conditionIsCurrent(theirs, parent.object?.metadata?.generation)
}

/**
 * The LocalQueue's reported state. Its text is always the LocalQueue's own
 * report; only the tone follows the parent, and only when the parent's stop
 * explains it.
 */
export function localQueueStatusWithParent(localQueue: any, parent: RelatedRead | undefined): StatusBadge {
  const own = getLocalQueueStatus(localQueue)
  if (localQueueInactiveBecauseParentStopped(localQueue, parent)) return { ...own, color: healthColors.neutral, level: 'neutral' }
  // While the parent read that decides the tone is in flight, don't flash the alert tone first.
  if (parent?.state === 'loading' && own.level === 'alert' && getKueueQueueCondition(localQueue)?.reason === 'ClusterQueueIsInactive') {
    return { ...own, color: healthColors.unknown, level: 'unknown' }
  }
  return own
}
