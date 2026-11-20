import { useLocation, useNavigate } from 'react-router-dom'
import { CustomResourceDefinitionRenderer as BaseRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/CustomResourceDefinitionRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui/types'
import { withCrossViewParams } from '../../../utils/navigation'

export function CustomResourceDefinitionRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const navigate = useNavigate()
  const { search } = useLocation()
  return <BaseRenderer data={data} onNavigate={onNavigate} onViewInstances={() => {
    navigate(withCrossViewParams(`/resources/${encodeURIComponent(data.spec.names.plural)}?apiGroup=${encodeURIComponent(data.spec.group)}`, search))
  }} />
}
