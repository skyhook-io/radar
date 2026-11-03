import { useMemo } from 'react'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import {
  ClusterQueueRenderer as BaseClusterQueueRenderer,
  LocalQueueRenderer as BaseLocalQueueRenderer,
  clusterQueueRelatedNames,
  type ClusterQueueReads,
} from '@skyhook-io/k8s-ui/components/resources/renderers/KueueQueueRenderers'
import { getClusterQueueCohort, isKueueQueueResource } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kueue'
import { needsNamespaceLabels } from '@skyhook-io/k8s-ui/components/kueue/eligibility'
import { localQueueStatusWithParent } from '@skyhook-io/k8s-ui/components/kueue/queue-status'
import type { RelatedRead } from '@skyhook-io/k8s-ui/components/kueue/related-read'
import { KUEUE_GROUP } from '@skyhook-io/k8s-ui/components/kueue/constants'
import { relatedReadKey, useRelatedObjects, useRelatedReads } from '../../kueue/useRelatedReads'

type Navigate = (ref: ResourceRef) => void

// Keyed by UID so moving the drawer to another queue starts its reads (and their mount time) afresh.
export function LocalQueueRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  return <LocalQueueReads key={data.metadata?.uid} data={data} onNavigate={onNavigate} />
}

function LocalQueueReads({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const parentName: string = data.spec?.clusterQueue ?? ''
  const namespace: string = data.metadata?.namespace ?? ''
  const parentRead = useRelatedObjects('clusterqueues', KUEUE_GROUP, undefined, [parentName], !!parentName, relatedReadKey(data, [parentName], ['Active'])).byName[parentName]
  const clusterQueue = parentName ? parentRead : undefined
  const needsLabels = clusterQueue?.state === 'found' && needsNamespaceLabels(clusterQueue.object?.spec?.namespaceSelector)
  const [namespaceRead] = useRelatedReads([{ kind: 'namespaces', name: namespace }], needsLabels && !!namespace)
  return <BaseLocalQueueRenderer data={data} onNavigate={onNavigate} reads={{ clusterQueue, namespace: needsLabels ? namespaceRead : undefined }} />
}

export function ClusterQueueRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  return <ClusterQueueReads key={data.metadata?.uid} data={data} onNavigate={onNavigate} />
}

function ClusterQueueReads({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const names = useMemo(() => clusterQueueRelatedNames(data), [data])
  const cohort = getClusterQueueCohort(data)
  const subjectKey = relatedReadKey(data, [...names.flavors, ...names.admissionChecks], ['Active'])
  const flavors = useRelatedObjects('resourceflavors', KUEUE_GROUP, undefined, names.flavors, names.flavors.length > 0, subjectKey)
  const checks = useRelatedObjects('admissionchecks', KUEUE_GROUP, undefined, names.admissionChecks, names.admissionChecks.length > 0, subjectKey)
  const queues = useRelatedObjects('clusterqueues', KUEUE_GROUP, undefined, [], cohort !== '-', relatedReadKey(data, [cohort], ['Active']))
  const members: RelatedRead<string[]> | undefined = cohort === '-' ? undefined
    : queues.all.state !== 'found' ? queues.all as RelatedRead<string[]>
      : {
          state: 'found',
          object: queues.all.object
            .filter(queue => queue?.metadata?.name !== data.metadata?.name && getClusterQueueCohort(queue) === cohort)
            .map(queue => String(queue.metadata.name))
            .sort(),
        }
  const reads: ClusterQueueReads = {
    flavors: flavors.byName,
    admissionChecks: checks.byName,
    cohortMembers: members,
    cohortMembersCompleteness: queues.completeness,
  }
  return <BaseClusterQueueRenderer data={data} onNavigate={onNavigate} reads={reads} />
}

/** The header badge: the LocalQueue's own report, toned like its detail by the ClusterQueue it names. */
export function LocalQueueStatusBadge({ data }: { data: any }) {
  const parentName: string = data?.spec?.clusterQueue ?? ''
  const supported = isKueueQueueResource(data)
  const parentRead = useRelatedObjects('clusterqueues', KUEUE_GROUP, undefined, [parentName], supported && !!parentName, relatedReadKey(data, [parentName], ['Active'])).byName[parentName]
  const parent = parentName ? parentRead : undefined
  const status = localQueueStatusWithParent(data, supported ? parent : undefined)
  return <span className={`badge ${status.color}`}>{status.text}</span>
}
