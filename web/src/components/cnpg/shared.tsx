import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { Database } from 'lucide-react'
import {
  CNPG_KIND_BY_KEY,
  FoldSection,
  coverageReadable,
  PaneLoader,
  RadarUpgradeAction,
  getRadarUpgradeRequirement,
  radarUpgradeDetail,
  radarUpgradeHeadline,
  type CNPGFleet,
  type CNPGKindCoverage,
  type CNPGWorkspaceResponse,
  type CNPGWorkspaceKey,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { Notice, RefreshFailedNotice, ScreenEmptyState } from '../workspace/layout'

export interface CNPGScreenProps {
  data: CNPGWorkspaceResponse
  fleet: CNPGFleet
  namespaces: string[]
  searchParams: URLSearchParams
  onSetParams: (update: Record<string, string | null>) => void
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
  onClearNamespaces: () => void
  /** Opens the create dialog for this screen's kind (the Clusters view's Create). */
  onCreate?: () => void
}

const COVERAGE_LABEL: Record<string, string> = {
  denied: 'no access',
  partial: 'not read in some namespaces',
  uncached: 'not cached by Radar',
  syncing: 'still loading',
  error: 'could not be read',
}

// The workspace name sits on the title line, as on the CloudNativePG detail
// pages; it is not a link, since on Clusters it would lead to itself.
export function CNPGWorkspaceHeader({ title, subtitle, actions }: { title: string; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="px-5 pt-4 xl:px-7">
      <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-2">
            <span className="flex items-center gap-1.5 text-sm text-theme-text-tertiary">
              <Database className="h-3.5 w-3.5" />
              CloudNativePG
              <span>/</span>
            </span>
            <h1 className="text-lg font-semibold text-theme-text-primary">{title}</h1>
          </div>
          {subtitle && <div className="mt-0.5 text-sm text-theme-text-secondary">{subtitle}</div>}
        </div>
        {actions}
      </div>
    </div>
  )
}

// The causes the server named for the namespaces it left unread. A partial or
// uncached read can name both kinds of namespace at once.
function namedCauses(cov: CNPGKindCoverage | undefined, uncachedNoun: string): { denied?: string; uncached?: string } {
  return {
    denied: cov?.deniedNamespaces?.length ? cov.deniedNamespaces.join(', ') : undefined,
    uncached: cov?.uncachedNamespaces?.length ? `${uncachedNoun} in ${cov.uncachedNamespaces.join(', ')}` : undefined,
  }
}

/** How much of a kind was read, in a few words ("not cached by Radar in pg"). */
export function coverageLabel(cov: CNPGKindCoverage | undefined): string {
  const { denied, uncached } = namedCauses(cov, 'not cached by Radar')
  const named = [denied && `no access in ${denied}`, uncached].filter(Boolean)
  if (named.length > 0) return named.join('; ')
  return COVERAGE_LABEL[cov?.state ?? ''] ?? cov?.state ?? 'unknown'
}

const INCOMPLETE_STATES = new Set(['partial', 'denied', 'syncing', 'error', 'uncached'])

/**
 * The incomplete kinds grouped by what was left unread, each reason said once.
 * Jobs count when their coverage is incomplete: a Cluster's initdb, join and
 * restore Job Pods are read only where they are.
 */
export function incompleteKindsText(kinds: CNPGFleet['incompleteKinds'], coverage: CNPGWorkspaceResponse['coverage'], jobCoverage?: CNPGKindCoverage): string {
  const byLabel = new Map<string, string[]>()
  const add = (kind: string, cov: CNPGKindCoverage | undefined) => {
    const label = coverageLabel(cov)
    byLabel.set(label, [...(byLabel.get(label) ?? []), kind])
  }
  for (const k of kinds) add(CNPG_KIND_BY_KEY[k].kind, coverage[k])
  if (jobCoverage && INCOMPLETE_STATES.has(jobCoverage.state)) add('Job', jobCoverage)
  return [...byLabel].map(([label, names]) => `${names.join(', ')} (${label})`).join('; ')
}

export function CoverageNotice({ fleet, data, kinds, namespace, includeJobs = false }: { fleet: CNPGFleet; data: CNPGWorkspaceResponse; kinds: readonly CNPGWorkspaceKey[]; namespace?: string; includeJobs?: boolean }) {
  const incomplete = fleet.incompleteKinds.filter((k) => { const coverage = data.coverage[k]; return kinds.includes(k) && (!coverage || !coverageReadable(coverage, namespace)) })
  const jobsIncomplete = includeJobs && !!data.jobCoverage && INCOMPLETE_STATES.has(data.jobCoverage.state) && !coverageReadable(data.jobCoverage, namespace)
  if (incomplete.length === 0 && !jobsIncomplete) return null
  return (
    <Notice>
      Some data used on this screen was not read.
      <FoldSection title="Coverage details" summary="Kinds and reasons" attention={false}>
        {incompleteKindsText(incomplete, data.coverage, jobsIncomplete ? data.jobCoverage : undefined)}. Counts over incomplete data are lower bounds.
      </FoldSection>
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
          title={radarUpgradeHeadline('CloudNativePG')}
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
        title="CloudNativePG data unavailable"
        detail={query.error instanceof Error ? query.error.message : 'Radar could not load the CloudNativePG data.'}
        action={
          <button type="button" onClick={() => void query.refetch()} disabled={query.isFetching} className="btn-secondary mt-3 px-3 py-1.5 text-sm">
            {query.isFetching ? 'Retrying…' : 'Retry'}
          </button>
        }
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
  return <><RefreshFailedNotice queries={[query]} className="px-5 pt-3 xl:px-7" />{children(data, fleet)}</>
}

/** Empty-state text for a collection, derived from how much of it was readable. */
export function coverageEmpty(cov: CNPGKindCoverage | undefined, noun: string): string {
  switch (cov?.state) {
    case 'full':
      return `No ${noun} in this scope.`
    case 'partial':
    case 'uncached': {
      const { denied, uncached } = namedCauses(cov, `Radar does not cache ${noun}`)
      const causes = [denied && `no access in ${denied}`, uncached].filter(Boolean)
      if (causes.length > 0) return `No ${noun} visible: ${causes.join('; ')}.`
      return cov.state === 'uncached' ? `Radar does not cache ${noun} in this scope.` : `No ${noun} visible. Some namespaces were not read.`
    }
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
