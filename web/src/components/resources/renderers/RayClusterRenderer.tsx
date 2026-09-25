import { useState } from 'react'
import { RayClusterRenderer as BaseRenderer, type RayClusterPodSelection } from '@skyhook-io/k8s-ui/components/resources/renderers/RayClusterRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useWorkloadPods } from '../../../api/client'

export function RayClusterRenderer(props: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  return <RayClusterEvidence key={props.data.metadata.uid} {...props} />
}

function RayClusterEvidence({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const [requestedSelection, setSelection] = useState<RayClusterPodSelection>({})
  const selection = requestedSelection.workerGroup != null && !(data.spec?.workerGroupSpecs ?? []).some((group: any) => group.groupName === requestedSelection.workerGroup) ? {} : requestedSelection
  const query = useWorkloadPods('rayclusters', data.metadata.namespace, data.metadata.uid ? data.metadata.name : '', {
    ownerUID: data.metadata.uid, ...selection, limit: 200, refetchInterval: 5000,
  })
  return <BaseRenderer data={data} onNavigate={onNavigate} selection={selection} onSelectPods={setSelection} podEvidence={query.data} podsLoading={query.isLoading} podsError={query.error?.message} />
}
