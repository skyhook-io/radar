import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { CNPGClusterSummary, PaneLoader, refToSelectedResource, type CNPGRef, type NavigateToResource } from '@skyhook-io/k8s-ui'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'
import { cnpgClusterFullPath } from './paths'

interface SummaryContext {
  apiKind: string
  namespace: string
  name: string
  group?: string
  resource: any
  context: 'drawer' | 'expanded'
  onNavigate?: NavigateToResource
}

function ClusterSummaryHost({ namespace, name, context, onNavigate }: SummaryContext) {
  const navigate = useNavigate()
  // The workspace is read for the object's own namespace: an explicitly opened
  // Cluster shows its facts whatever the namespace filter is.
  const { query, fleet } = useCNPGFleet([namespace])
  const row = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)
  if (!row) {
    if (query.isLoading) return <PaneLoader label="Loading summary…" className="h-40" />
    return (
      <div className="px-4 py-4 text-sm text-theme-text-secondary">
        {query.error instanceof Error
          ? `The CloudNativePG summary could not be loaded: ${query.error.message}`
          : 'This Cluster is not in the CloudNativePG workspace for your identity.'}{' '}
        Spec & status still shows everything the object reports.
      </div>
    )
  }
  const go = onNavigate ? (ref: CNPGRef) => onNavigate(refToSelectedResource(ref)) : undefined
  return (
    <CNPGClusterSummary
      row={row}
      onNavigate={go}
      actions={context === 'drawer' ? [{ label: 'Open cluster', primary: true, onClick: () => navigate(cnpgClusterFullPath(namespace, name)) }] : undefined}
    />
  )
}

/**
 * The composed Overview for CloudNativePG kinds. Returns null for kinds
 * without one, which keeps the default Overview.
 */
export function renderCNPGSummary(ctx: SummaryContext): ReactNode {
  if (ctx.group !== 'postgresql.cnpg.io') return null
  if (ctx.resource?.kind === 'Cluster') return <ClusterSummaryHost {...ctx} />
  return null
}
