import type { QueryClient, QueryFilters } from '@tanstack/react-query'
import type { IntegrationKind } from './LocalConnectionSettings'

const metricsKeys = new Set([
  'workload-metrics', 'substrate-health', 'dashboard',
  'traffic-sources', 'traffic-flows', 'traffic-source-active', 'traffic-connection',
])
const cnpgMetricsKeys = ['history', 'fleet-metrics', 'storage', 'disk']
const argoKeys = new Set([
  'argocd-status', 'gitops-tree', 'gitops-insights', 'argo-resource-diff',
  'argo-sync-diff', 'argo-revision-metadata',
])

export async function invalidateIntegrationQueries(
  client: QueryClient,
  kinds: IntegrationKind[],
  { includeStatus = true } = {},
): Promise<void> {
  const metrics = kinds.includes('metrics')
  const cost = metrics || kinds.includes('cost')
  const argo = kinds.includes('argocd')
  const filters: QueryFilters = {
    predicate: ({ queryKey: [root, section] }) => {
      if (typeof root !== 'string') return false
      if (root === 'diagnostics') return kinds.length > 0
      if (cost && root.startsWith('opencost-')) return true
      if (argo && argoKeys.has(root)) return true
      if (!metrics) return false
      if (root === 'prometheus-status') return includeStatus
      return root.startsWith('prometheus-') || metricsKeys.has(root) ||
        (root === 'cnpg' && typeof section === 'string' && cnpgMetricsKeys.includes(section))
    },
  }
  await client.cancelQueries(filters)
  await client.invalidateQueries(filters)
}
