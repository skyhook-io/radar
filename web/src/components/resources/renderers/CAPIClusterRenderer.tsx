import { CAPIClusterRenderer as BaseCAPIClusterRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/CAPIClusterRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { apiFetch, useCapabilities } from '../../../api/client'
import { apiUrl } from '../../../api/config'

const request = (path: string, init?: RequestInit) => apiFetch(apiUrl(path), init)

export function CAPIClusterRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const { data: capabilities } = useCapabilities()
  return <BaseCAPIClusterRenderer data={data} onNavigate={onNavigate} request={request} canConnect={capabilities != null && capabilities.configManagement !== 'operator'} />
}
