import { useCallback } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { K3sHelmChartRenderer as BaseK3sHelmChartRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/K3sHelmRenderers'
import type { ResourceRef } from '@skyhook-io/k8s-ui'

/** Host wrapper: the HelmChart drawer's "Open Helm release" goes to the Helm view, keeping the namespace pick. */
export function K3sHelmChartRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const openRelease = useCallback((ref: { namespace: string; name: string }) => {
    const params = new URLSearchParams()
    const namespaces = searchParams.get('namespaces')
    if (namespaces) params.set('namespaces', namespaces)
    params.set('release', `${ref.namespace}/${ref.name}`)
    navigate({ pathname: '/helm', search: params.toString() })
  }, [navigate, searchParams])
  return <BaseK3sHelmChartRenderer data={data} onNavigate={onNavigate} onOpenHelmRelease={openRelease} />
}
