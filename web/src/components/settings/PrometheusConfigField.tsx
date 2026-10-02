import { useEffect, useRef, type ComponentProps } from 'react'
import { apiUrl, getApiBase, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { PrometheusConnectionForm, type PrometheusApplyResult } from './PrometheusConnectionForm'

export function PrometheusConfigField({ onBusyChange, ...props }: Omit<ComponentProps<typeof PrometheusConnectionForm>, 'onApply'> & { onBusyChange?: (busy: boolean) => void }) {
  const request = useRef<AbortController | null>(null)
  useEffect(() => () => request.current?.abort(), [])
  useEffect(() => () => onBusyChange?.(false), [onBusyChange])
  const apply = async (url: string, headers?: Record<string, string>): Promise<PrometheusApplyResult> => {
    if (request.current) throw new Error('A connection update is already in progress.')
    const controller = new AbortController(); request.current = controller
    const base = getApiBase()
    onBusyChange?.(true)
    try {
      const response = await fetch(apiUrl('/integrations/prometheus'), {
        method: 'PUT', signal: controller.signal, credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify({ prometheusUrl: url, ...(headers !== undefined ? { headers } : {}) }),
      })
      const data = await response.json().catch(() => ({})) as PrometheusApplyResult
      if (controller.signal.aborted || base !== getApiBase()) throw new Error('Cluster changed; reload Settings.')
      if (!response.ok) throw new Error(data.error || `Request failed (${response.status})`)
      return data
    } finally {
      if (request.current === controller) request.current = null
      if (!controller.signal.aborted) onBusyChange?.(false)
    }
  }
  return <PrometheusConnectionForm {...props} onApply={apply} />
}
