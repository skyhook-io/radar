import { useOpenCostSummary, usePrometheusStatus, type useArgoStatus } from '../../api/client'
import { costIntegrationUnavailableMessage, costSourceLabel } from '../cost/source'
import type { IntegrationKind, IntegrationProfile } from './LocalConnectionSettings'

export function LocalIntegrationStatus({ kind, profile, argo, busy }: {
  kind: IntegrationKind
  profile: IntegrationProfile
  argo: ReturnType<typeof useArgoStatus>
  busy: boolean
}) {
  const prom = usePrometheusStatus(kind === 'metrics')
  const cost = useOpenCostSummary(kind === 'cost')
  const query = kind === 'metrics' ? prom : kind === 'cost' ? cost : argo
  const selection = profile.state === 'launch' ? 'Startup configuration'
    : kind === 'cost' ? profile.mode === 'prometheus' ? 'OpenCost metrics'
      : profile.mode === 'kubecost' ? 'Kubecost' : 'Automatic source selection'
    : profile.url ? 'Configured backend' : 'Automatic discovery'
  let status = 'Checking…'
  let detail: string | undefined
  if (!busy && !query.isFetching && !query.isPlaceholderData) {
    if (query.isError) status = 'Status unavailable'
    else if (kind === 'metrics' && prom.data) {
      status = prom.data.connected ? 'Connected' : prom.data.discovering ? 'Discovering…' : 'Not connected'
      detail = prom.data.connected ? prom.data.address : prom.data.error
    } else if (kind === 'argocd' && argo.data) {
      status = argo.data.connected ? argo.data.anonymous ? 'Connected · no token needed' : 'Connected' : 'Not connected'
      detail = argo.data.connected ? argo.data.address : argo.data.reason
    } else if (kind === 'cost' && cost.data) {
      status = cost.data.available ? `Available · ${costSourceLabel(cost.data.source)}`
        : ['no_prometheus', 'no_cost_source', 'no_metrics'].includes(cost.data.reason ?? '') ? 'No cost data' : 'Unavailable'
      detail = cost.data.reason ? costIntegrationUnavailableMessage(cost.data.reason) ?? undefined : undefined
    }
  }
  return (
    <div role="group" className="space-y-1 text-xs text-theme-text-secondary" aria-label="Current connection">
      <p><span className="font-medium">Current connection</span> · {selection} · {status}</p>
      {detail && <p className="break-words text-theme-text-tertiary">{detail}</p>}
    </div>
  )
}
