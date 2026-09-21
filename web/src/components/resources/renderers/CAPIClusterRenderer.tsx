import { CAPIClusterRenderer as BaseCAPIClusterRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/CAPIClusterRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useCapabilities } from '../../../api/client'

export function CAPIClusterRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const { data: capabilities } = useCapabilities()
  return <BaseCAPIClusterRenderer data={data} onNavigate={onNavigate} canConnect={capabilities != null && capabilities.configManagement !== 'operator'} />
}
