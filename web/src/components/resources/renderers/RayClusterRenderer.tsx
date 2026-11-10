import { useState } from 'react'
import { RayClusterRenderer as BaseRenderer, type RayClusterPodSelection } from '@skyhook-io/k8s-ui/components/resources/renderers/RayClusterRenderer'
import type { ResourceRef, WorkloadPodInfo } from '@skyhook-io/k8s-ui'
import { isStableClientError, useRadarFeature, useWorkloadPods } from '../../../api/client'
import { useOpenLogs } from '../../dock'
import { logsContainer } from '@skyhook-io/k8s-ui/components/resources/resource-utils-ray'
import { rayClusterPodsError, useLogsReadability, useSidecarsMarked } from '../../logs/rayClusterPods'

export function RayClusterRenderer(props: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  return <RayClusterEvidence key={props.data.metadata.uid} {...props} />
}

function RayClusterEvidence({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const [requestedSelection, setSelection] = useState<RayClusterPodSelection>({})
  const selection = requestedSelection.workerGroup != null && !(data.spec?.workerGroupSpecs ?? []).some((group: any) => group.groupName === requestedSelection.workerGroup) ? {} : requestedSelection
  const { support, upgrade } = useRadarFeature('rayClusterPods')
  const query = useWorkloadPods('rayclusters', data.metadata.namespace, data.metadata.uid && support !== 'unsupported' ? data.metadata.name : '', {
    ownerUID: data.metadata.uid, ...selection, limit: 200, refetchInterval: 5000,
  })
  const logsReadable = useLogsReadability(data.metadata.namespace) !== 'denied'
  // A refresh that fails for a passing reason keeps the last list, as the Logs tab does.
  const kept = query.data && query.error && !isStableClientError(query.error)
  const openLogs = useOpenLogs()
  const sidecarsMarked = useSidecarsMarked()
  const openPodLogs = (pod: WorkloadPodInfo) => openLogs({
    namespace: data.metadata.namespace,
    podName: pod.name,
    containers: pod.containers,
    containerName: logsContainer(pod, sidecarsMarked),
  })
  return <BaseRenderer data={data} onNavigate={onNavigate} selection={selection} onSelectPods={setSelection} podEvidence={query.data} podsLoading={query.isLoading} podsError={kept ? undefined : rayClusterPodsError(query.error, support)} podsNote={kept ? `Pod list not refreshed: ${rayClusterPodsError(query.error, support)}` : undefined} onOpenPodLogs={logsReadable ? openPodLogs : undefined} podLogsNote={logsReadable ? undefined : 'Pod logs need get on pods/log in this namespace.'} podsUpgrade={support === 'unsupported' ? upgrade : null} />
}
