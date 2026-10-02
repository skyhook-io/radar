import { useOpenCostSummary, usePrometheusStatus, type useArgoStatus } from '../../api/client'
import { costIntegrationUnavailableMessage, costSourceLabel } from '../cost/source'
import type { IntegrationKind, IntegrationProfile } from './LocalConnectionSettings'
import { Badge, Collapse } from '@skyhook-io/k8s-ui'
import { useRef } from 'react'

export function LocalIntegrationStatus({ kind, profile, argo, busy, changedAt }: {
  kind: IntegrationKind
  profile: IntegrationProfile
  argo: ReturnType<typeof useArgoStatus>
  busy: boolean
  changedAt: number
}) {
  const prom = usePrometheusStatus(kind === 'metrics')
  const cost = useOpenCostSummary(kind === 'cost')
  const query = kind === 'metrics' ? prom : kind === 'cost' ? cost : argo
  // Background polls keep the last status; only the first load, or a result
  // older than the latest saved integration change, reads as "Checking…".
  const checking = busy || query.isPlaceholderData || (query.isFetching && !query.data) ||
    Math.max(query.dataUpdatedAt, query.errorUpdatedAt) < changedAt
  let status = 'Checking…'
  let detail: string | undefined
  if (!checking) {
    if (query.isError) status = 'Status unavailable'
    else if (kind === 'metrics' && prom.data) {
      status = prom.data.connected ? 'Connected' : prom.data.discovering ? 'Discovering…' : 'Not connected'
      detail = prom.data.connected ? !profile.url ? `Discovered: ${prom.data.address}` : undefined : prom.data.error
    } else if (kind === 'argocd' && argo.data) {
      status = argo.data.connected ? argo.data.anonymous ? 'Connected · no token needed' : 'Connected' : 'Not connected'
      detail = argo.data.connected ? !profile.url ? `Discovered: ${argo.data.address}` : undefined : argo.data.reason
    } else if (kind === 'cost' && cost.data) {
      status = cost.data.available ? `Available · ${costSourceLabel(cost.data.source)}`
        : ['no_prometheus', 'no_cost_source', 'no_metrics'].includes(cost.data.reason ?? '') ? 'No cost data' : 'Unavailable'
      detail = cost.data.reason ? costIntegrationUnavailableMessage(cost.data.reason)
        ?? (cost.data.reason === 'query_error' ? 'The active cost source query failed.' : undefined) : undefined
    }
  }
  const lastDetail = useRef({ text: detail, visible: !!detail })
  if (!checking) lastDetail.current = { text: detail ?? lastDetail.current.text, visible: !!detail }
  const configured = !!(profile.url || profile.secretSet || profile.headerKeys.length || profile.clusterId || profile.mode === 'prometheus')
  const failed = !checking && (query.isError || configured && ['Not connected', 'Unavailable', 'No cost data'].includes(status))
  return (
    <div role="group" className="contents text-xs text-theme-text-secondary" aria-label="Current connection">
      <Badge severity={status.startsWith('Connected') || status.startsWith('Available') ? 'success' : failed ? 'warning' : 'neutral'}>{status}</Badge>
      <Collapse open={lastDetail.current.visible} className="basis-full min-w-0">
        <p className={`break-words text-theme-text-tertiary ${checking ? 'invisible' : ''}`}>{lastDetail.current.text}</p>
      </Collapse>
    </div>
  )
}
