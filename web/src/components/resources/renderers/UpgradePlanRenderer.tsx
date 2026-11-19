import { UpgradePlanRenderer as BaseUpgradePlanRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/UpgradePlanRenderer'
import type { PlanNode } from '@skyhook-io/k8s-ui/components/resources/resource-utils-upgrade-plan'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import type { ResourceCoverage } from '@skyhook-io/k8s-ui/types/resource-coverage'
import { useResources } from '../../../api/client'

// A Node read the caller was denied, or one that covered only part of the
// cluster, is unreadable: progress is then unknown, never "0 of 0". An older
// Radar reports no coverage; Nodes are cluster-scoped, so its list is whole.
export function toPlanNodes(query: { data?: any[]; coverage?: ResourceCoverage; error: unknown; isLoading: boolean }): { nodes: PlanNode[] | null | undefined; loading: boolean } {
  if (query.error) return { nodes: null, loading: false }
  if (query.isLoading && !query.data) return { nodes: undefined, loading: true }
  if (!query.data || (query.coverage && query.coverage.status !== 'complete' && query.coverage.status !== 'unknown')) return { nodes: null, loading: false }
  return { nodes: query.data.map((n) => ({ name: n?.metadata?.name ?? '', labels: n?.metadata?.labels ?? {} })), loading: false }
}

/** Host wrapper: feeds the cluster's Nodes to the Plan drawer's per-node progress. */
export function UpgradePlanRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const query = useResources<any>('nodes')
  const { nodes, loading } = toPlanNodes({ data: query.data, coverage: query.coverage, error: query.error, isLoading: query.isLoading })
  return <BaseUpgradePlanRenderer data={data} onNavigate={onNavigate} nodes={nodes} nodesLoading={loading} />
}
