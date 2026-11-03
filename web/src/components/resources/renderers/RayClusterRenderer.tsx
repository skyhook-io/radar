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
    containerName: logsContainer(pod),
  })
  return <BaseRenderer data={data} onNavigate={onNavigate} selection={selection} onSelectPods={setSelection} podEvidence={query.data} podsLoading={query.isLoading} podsError={query.error?.message} onOpenPodLogs={canViewLogs ? openPodLogs : undefined} podsUpgrade={support === 'unsupported' ? upgrade : null} />
}

// The container a Pod's logs open on. Container lists put init containers
// first and only reported statuses say which those are, so with no statuses
// the viewer keeps its own default. While no application container has run
// (none ready or restarted, and the Pod hasn't finished), the first unready
// init container is what the Pod is waiting on — a worker waiting for the head sits in KubeRay's
// wait-gcs-ready — though it may itself not have started yet. Otherwise the
// first application container, so a running native sidecar never wins over
// a crashing application.
export function logsContainer(pod: WorkloadPodInfo): string | undefined {
  const statuses = pod.containerStatuses ?? []
  if (!statuses.length) return undefined
  const init = new Set(statuses.filter(status => status.init).map(status => status.name))
  const appRan = pod.phase === 'Succeeded' || pod.phase === 'Failed' || statuses.some(status => !status.init && (status.ready || status.restartCount > 0))
  const blockingInit = appRan ? undefined : pod.containers.find(name => statuses.some(status => status.init && status.name === name && !status.ready))
  return blockingInit ?? pod.containers.find(name => !init.has(name))
}
