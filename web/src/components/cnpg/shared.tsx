import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { Database } from 'lucide-react'
import {
  CNPG_KIND_BY_KEY,
  PaneLoader,
  RadarUpgradeAction,
  getRadarUpgradeRequirement,
  radarUpgradeDetail,
  radarUpgradeHeadline,
  type CNPGFleet,
  type CNPGKindCoverage,
  type CNPGWorkspaceResponse,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { Notice, ScreenEmptyState } from '../workspace/layout'

export interface CNPGScreenProps {
  data: CNPGWorkspaceResponse
  fleet: CNPGFleet
  namespaces: string[]
  searchParams: URLSearchParams
  onSetParams: (update: Record<string, string | null>) => void
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
  onClearNamespaces: () => void
}

const COVERAGE_LABEL: Record<string, string> = {
  denied: 'no access',
  partial: 'not read in some namespaces',
  uncached: 'not cached by Radar',
  syncing: 'still loading',
  error: 'could not be read',
}

export function CNPGWorkspaceHeader({ title, subtitle, actions }: { title: string; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="px-5 pt-4 xl:px-7">
      <div className="flex items-center gap-2 text-xs text-theme-text-tertiary">
        <Database className="h-3.5 w-3.5" />
        <span>CloudNativePG</span>
      </div>
      <div className="mt-1 flex flex-wrap items-end justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <h1 className="text-lg font-semibold text-theme-text-primary">{title}</h1>
          {subtitle && <div className="mt-0.5 text-sm text-theme-text-secondary">{subtitle}</div>}
        </div>
        {actions}
      </div>
    </div>
  )
}

export function CoverageNotice({ fleet, data }: { fleet: CNPGFleet; data: CNPGWorkspaceResponse }) {
  if (fleet.incompleteKinds.length === 0) return null
  const parts = fleet.incompleteKinds.map((k) => {
    const cov = data.coverage[k]
    const label = COVERAGE_LABEL[cov?.state ?? ''] ?? cov?.state
    return `${CNPG_KIND_BY_KEY[k].kind} (${label})`
  })
  return (
    <Notice>
      Some CloudNativePG data is not readable: {parts.join(', ')}. Facts built on it read “No access” or “unknown” rather than none, and counts are lower bounds.
    </Notice>
  )
}

/**
 * Loading, error and not-installed states every workspace screen shares.
 * Renders the screen only once there is workspace data to render.
 */
export function CNPGScreenGate({
  query,
  fleet,
  children,
}: {
  query: UseQueryResult<CNPGWorkspaceResponse>
  fleet: CNPGFleet | null
  children: (data: CNPGWorkspaceResponse, fleet: CNPGFleet) => ReactNode
}) {
  const { connection } = useConnection()
  const data = query.data
  if (!data && query.isLoading) return <PaneLoader label="Loading CloudNativePG…" className="flex-1" />
  if (!data) {
    const upgrade = getRadarUpgradeRequirement(query.error)
    if (upgrade) {
      return (
        <ScreenEmptyState
          icon={Database}
          title={radarUpgradeHeadline('The CloudNativePG workspace')}
          detail={radarUpgradeDetail(upgrade)}
          action={
            <div className="mt-3 text-sm">
              <RadarUpgradeAction requirement={upgrade} />
            </div>
          }
        />
      )
    }
    return (
      <ScreenEmptyState
        icon={Database}
        title="CloudNativePG workspace unavailable"
        detail={query.error instanceof Error ? query.error.message : 'The workspace could not be loaded.'}
      />
    )
  }
  if (!data.installed || !fleet) {
    return (
      <ScreenEmptyState
        icon={Database}
        title="CloudNativePG is not installed"
        detail={`No postgresql.cnpg.io resources are served in ${connection.context || 'this cluster'}.`}
      />
    )
  }
  return <>{children(data, fleet)}</>
}

/** Empty-state text for a collection, derived from how much of it was readable. */
export function coverageEmpty(cov: CNPGKindCoverage | undefined, noun: string): string {
  switch (cov?.state) {
    case 'full':
      return `No ${noun} in this scope.`
    case 'partial':
      if (cov.uncachedNamespaces?.length) return `No ${noun} visible. Radar does not cache ${noun} in ${cov.uncachedNamespaces.join(', ')}.`
      if (cov.deniedNamespaces?.length) return `No ${noun} visible. Some namespaces are not readable with your access.`
      return `No ${noun} visible. Some namespaces were not read.`
    case 'uncached':
      return `Radar does not cache ${noun} in this scope.`
    case 'denied':
      return `No access to ${noun}.`
    case 'syncing':
      return `${noun[0].toUpperCase()}${noun.slice(1)} are still loading.`
    case 'error':
      return `${noun[0].toUpperCase()}${noun.slice(1)} could not be read.`
    default:
      return `This kind is not installed.`
  }
}

/** The less complete of two coverages, for collections built from several kinds. */
export function worstCoverage(...covs: (CNPGKindCoverage | undefined)[]): CNPGKindCoverage | undefined {
  const rank: Record<string, number> = { error: 0, denied: 1, uncached: 2, syncing: 3, partial: 4, full: 5, notInstalled: 6 }
  return covs.filter(Boolean).sort((a, b) => (rank[a!.state] ?? 9) - (rank[b!.state] ?? 9))[0]
}

export function clusterResource(namespace: string, name: string): SelectedResource {
  return { kind: 'clusters', group: 'postgresql.cnpg.io', namespace, name }
}

export function cnpgResource(plural: string, namespace: string, name: string, group = 'postgresql.cnpg.io'): SelectedResource {
  return { kind: plural, group, namespace, name }
}
