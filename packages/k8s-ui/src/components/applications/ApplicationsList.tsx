import { useMemo, type ReactNode } from 'react'
import { type AppRow, buildSingleAppEntry } from '../../utils/applications'
import { ApplicationsView } from './ApplicationsView'

// ApplicationsList — the OSS single-cluster Applications view. A thin wrapper
// that maps wire rows to single-cluster entries and drives the shared
// ApplicationsView core (the facet rail, hero header, fold, search, sort, and
// keyboard nav all live there). Data + selection are injected. The Cloud fleet
// view is a sibling wrapper over the same core with variant="fleet".

export interface ApplicationsListProps {
  apps: AppRow[]
  onSelect: (key: string) => void
  /** Leading element in the header actions (e.g. a freshness control). */
  headerActions?: ReactNode
  /** First fetch in flight — chassis renders its shape-stable skeleton. */
  loading?: boolean
}

export function ApplicationsList({ apps, onSelect, headerActions, loading }: ApplicationsListProps) {
  // Env tokens this CLUSTER proved (identity classifications on the wire) feed
  // the namespace heuristic, so sibling-less rows in discovered env namespaces
  // still label without any hardcoded vocabulary.
  const discoveredEnvs = useMemo(() => new Set(apps.map((a) => a.identity?.env).filter((e): e is string => !!e)), [apps])
  const entries = useMemo(() => apps.map((a) => buildSingleAppEntry(a, discoveredEnvs)), [apps, discoveredEnvs])

  return (
    <ApplicationsView
      loading={loading}
      variant="single"
      entries={entries}
      onSelect={onSelect}
      headerActions={headerActions}
      title="Applications"
      description="Deployable software in this cluster — your services, workers, and jobs, grouped by app/release evidence."
    />
  )
}
