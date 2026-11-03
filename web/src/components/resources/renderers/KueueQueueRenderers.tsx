import { useMemo } from 'react'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import {
  ClusterQueueRenderer as BaseClusterQueueRenderer,
  LocalQueueRenderer as BaseLocalQueueRenderer,
  clusterQueueRelatedNames,
  type ClusterQueueReads,
} from '@skyhook-io/k8s-ui/components/resources/renderers/KueueQueueRenderers'
import { getClusterQueueCohort } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kueue'
import { selectorHasTerms } from '@skyhook-io/k8s-ui/components/kueue/eligibility'
import { localQueueStatusWithParent } from '@skyhook-io/k8s-ui/components/kueue/queue-status'
import { isKueueQueueResource } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kueue'
import type { RelatedRead } from '@skyhook-io/k8s-ui/components/kueue/related-read'
import { useRelatedList, useRelatedReads } from '../../kueue/useRelatedReads'

const KUEUE_GROUP = 'kueue.x-k8s.io'

type Navigate = (ref: ResourceRef) => void

export function LocalQueueRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const parentName: string = data.spec?.clusterQueue ?? ''
  const namespace: string = data.metadata?.namespace ?? ''
  const clusterQueue = useRelatedList('clusterqueues', KUEUE_GROUP, undefined, !!parentName).find(parentName)
  const needsLabels = clusterQueue.state === 'found' && selectorHasTerms(clusterQueue.object?.spec?.namespaceSelector)
  const [namespaceRead] = useRelatedReads([{ kind: 'namespaces', name: namespace }], needsLabels && !!namespace)
  return <BaseLocalQueueRenderer data={data} onNavigate={onNavigate} reads={{ clusterQueue, namespace: needsLabels ? namespaceRead : undefined }} />
}

export function ClusterQueueRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const names = useMemo(() => clusterQueueRelatedNames(data), [data])
  const cohort = getClusterQueueCohort(data)
  const flavors = useRelatedList('resourceflavors', KUEUE_GROUP, undefined, names.flavors.length > 0)
  const checks = useRelatedList('admissionchecks', KUEUE_GROUP, undefined, names.admissionChecks.length > 0)
  const queues = useRelatedList('clusterqueues', KUEUE_GROUP, undefined, cohort !== '-')
  const members = useMemo((): RelatedRead<string[]> | undefined => {
    if (cohort === '-') return undefined
    if (queues.all.state !== 'found') return queues.all as RelatedRead<string[]>
    const others = queues.all.object
      .filter(queue => queue?.metadata?.name !== data.metadata?.name && getClusterQueueCohort(queue) === cohort)
      .map(queue => String(queue.metadata.name))
      .sort()
    return { state: 'found', object: others }
  }, [cohort, queues, data.metadata?.name])
  const reads: ClusterQueueReads = {
    flavors: Object.fromEntries(names.flavors.map(name => [name, flavors.find(name)])),
    admissionChecks: Object.fromEntries(names.admissionChecks.map(name => [name, checks.find(name)])),
    cohortMembers: members,
    cohortMembersComplete: queues.complete,
  }
  return <BaseClusterQueueRenderer data={data} onNavigate={onNavigate} reads={reads} />
}

/** The header badge: the LocalQueue's own report, toned like its detail by the ClusterQueue it names. */
export function LocalQueueStatusBadge({ data }: { data: any }) {
  const parentName: string = data?.spec?.clusterQueue ?? ''
  const supported = isKueueQueueResource(data)
  const parent = useRelatedList('clusterqueues', KUEUE_GROUP, undefined, supported && !!parentName).find(parentName)
  const status = localQueueStatusWithParent(data, supported ? parent : undefined)
  return <span className={`badge ${status.color}`}>{status.text}</span>
}
