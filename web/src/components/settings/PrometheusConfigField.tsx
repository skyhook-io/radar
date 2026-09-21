import { useEffect, useRef, type ComponentProps } from 'react'
import { apiUrl, getApiBase, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { PrometheusConnectionForm, type PrometheusApplyResult } from './PrometheusConnectionForm'

export function PrometheusConfigField(props: Omit<ComponentProps<typeof PrometheusConnectionForm>, 'onApply'>) {
  const request = useRef<AbortController | null>(null)
  useEffect(() => () => request.current?.abort(), [])
  const apply = async (url: string, headers?: Record<string, string>): Promise<PrometheusApplyResult> => {
    if (request.current) throw new Error('A connection update is already in progress.')
    const controller = new AbortController(); request.current = controller
    const base = getApiBase()
    try {
      const response = await fetch(apiUrl('/integrations/prometheus'), {
        method: 'PUT', signal: controller.signal, credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify({ prometheusUrl: url, ...(headers !== undefined ? { headers } : {}) }),
      })
      const data = await response.json() as PrometheusApplyResult
      if (controller.signal.aborted || base !== getApiBase()) throw new Error('Cluster changed; reload Settings.')
      if (!response.ok) throw new Error(data.error || response.statusText)
      return data
    } finally { if (request.current === controller) request.current = null }
  }
  return <PrometheusConnectionForm {...props} onApply={apply} />
}
