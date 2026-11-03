import { useState } from 'react'
import { RayClusterRenderer as BaseRenderer, type RayClusterPodSelection } from '@skyhook-io/k8s-ui/components/resources/renderers/RayClusterRenderer'
import type { ResourceRef, WorkloadPodInfo } from '@skyhook-io/k8s-ui'
import { useRadarFeature, useWorkloadPods } from '../../../api/client'
import { useNamespacedCapabilities } from '../../../contexts/CapabilitiesContext'
import { useOpenLogs } from '../../dock'

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
  const { canViewLogs } = useNamespacedCapabilities(data.metadata.namespace)
  const openLogs = useOpenLogs()
  const openPodLogs = (pod: WorkloadPodInfo) => openLogs({
    namespace: data.metadata.namespace,
    podName: pod.name,
    containers: pod.containers,
    containerName: firstAppContainer(pod),
  })
  return <BaseRenderer data={data} onNavigate={onNavigate} selection={selection} onSelectPods={setSelection} podEvidence={query.data} podsLoading={query.isLoading} podsError={query.error?.message} onOpenPodLogs={canViewLogs ? openPodLogs : undefined} podsUpgrade={support === 'unsupported' ? upgrade : null} />
}

// Pod container lists put init containers first; only reported statuses say
// which ones they are, so without them the viewer keeps its own default.
export function firstAppContainer(pod: WorkloadPodInfo): string | undefined {
  if (!pod.containerStatuses?.length) return undefined
  const init = new Set(pod.containerStatuses.filter(status => status.init).map(status => status.name))
  return pod.containers.find(name => !init.has(name))
}
