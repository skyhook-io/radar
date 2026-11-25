import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { HardDrive } from 'lucide-react'
import {
  PaneLoader,
  RadarUpgradeAction,
  getRadarUpgradeRequirement,
  radarUpgradeDetail,
  radarUpgradeHeadline,
  type LonghornVolumeHealth,
  type LonghornWorkspaceKey,
  type LonghornWorkspaceResponse,
  type StatusTone,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { refToSelectedResource } from '../../utils/navigation'
import { Notice, RefreshFailedNotice, ScreenEmptyState } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES, coverageLabel } from '../workspace/coverage'
import { LONGHORN_GROUP } from './routes'

export interface LonghornScreenProps {
  data: LonghornWorkspaceResponse
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
}

/** Degraded is routine while replicas rebuild, so it stays amber; faulted is the red one; detached is no problem. */
export const LONGHORN_HEALTH: Record<LonghornVolumeHealth, { label: string; tone: StatusTone }> = {
  healthy: { label: 'Healthy', tone: 'healthy' },
  degraded: { label: 'Degraded', tone: 'degraded' },
  faulted: { label: 'Faulted', tone: 'unhealthy' },
  detached: { label: 'Detached', tone: 'neutral' },
  unknown: { label: 'Unknown', tone: 'unknown' },
}

export function longhornVolumeResource(namespace: string, name: string): SelectedResource {
  return refToSelectedResource({ kind: 'Volume', group: LONGHORN_GROUP, namespace, name })
}

export function LonghornHeader({ title, data }: { title: string; data: LonghornWorkspaceResponse }) {
  return (
    <div className="px-5 pt-4 xl:px-7">
      <div className="flex flex-wrap items-center gap-x-2">
        <span className="flex items-center gap-1.5 text-sm text-theme-text-tertiary">
          <HardDrive className="h-3.5 w-3.5" />
          Longhorn
          <span>/</span>
        </span>
        <h1 className="text-lg font-semibold text-theme-text-primary">{title}</h1>
      </div>
      {data.forcedNamespace && (
        <div className="mt-0.5 text-sm text-theme-text-secondary">Radar is limited to namespace {data.forcedNamespace}, so only Longhorn volumes there are shown.</div>
      )}
    </div>
  )
}

const KIND_LABEL: Record<LonghornWorkspaceKey, string> = { volumes: 'Volume', engines: 'Engine', persistentVolumes: 'PersistentVolume' }

/** Says which kinds were not read in full, and why. */
export function LonghornCoverageNotice({ data }: { data: LonghornWorkspaceResponse }) {
  const byLabel = new Map<string, string[]>()
  for (const k of Object.keys(KIND_LABEL) as LonghornWorkspaceKey[]) {
    const cov = data.coverage[k]
    if (cov && !INCOMPLETE_COVERAGE_STATES.has(cov.state)) continue
    const label = coverageLabel(cov)
    byLabel.set(label, [...(byLabel.get(label) ?? []), KIND_LABEL[k]])
  }
  if (byLabel.size === 0) return null
  const text = [...byLabel].map(([label, names]) => `${names.join(', ')} (${label})`).join('; ')
  return <Notice>Radar couldn’t read some of the Longhorn data on this screen: {text}. What it couldn’t read shows as unknown.</Notice>
}

/** Loading, error, needs-newer-Radar and not-installed states; renders the screen once there is data. */
export function LonghornScreenGate({
  query,
  discoveryPending,
  discovered,
  children,
}: {
  query: Pick<UseQueryResult<LonghornWorkspaceResponse>, 'data' | 'error' | 'isLoading' | 'isFetching' | 'refetch' | 'isRefetchError' | 'dataUpdatedAt'>
  discoveryPending: boolean
  /** Discovery serves longhorn.io, or could not be read (the server is asked then). */
  discovered: boolean
  children: (data: LonghornWorkspaceResponse) => ReactNode
}) {
  const { connection } = useConnection()
  const data = query.data
  const notInstalled = <ScreenEmptyState icon={HardDrive} title="Longhorn is not installed" detail={`No longhorn.io resources are served in ${connection.context || 'this cluster'}.`} />
  if (!data && (query.isLoading || discoveryPending)) return <PaneLoader label="Loading Longhorn…" className="flex-1" />
  if (!data && !discovered) return notInstalled
  if (!data) {
    const upgrade = getRadarUpgradeRequirement(query.error)
    if (upgrade) {
      return (
        <ScreenEmptyState
          icon={HardDrive}
          title={radarUpgradeHeadline('Longhorn')}
          detail={radarUpgradeDetail(upgrade)}
          action={<div className="mt-3 text-sm"><RadarUpgradeAction requirement={upgrade} /></div>}
        />
      )
    }
    return (
      <ScreenEmptyState
        icon={HardDrive}
        title="Longhorn data unavailable"
        detail={query.error instanceof Error ? query.error.message : 'Radar could not load the Longhorn data.'}
        action={
          <button type="button" onClick={() => void query.refetch()} disabled={query.isFetching} className="btn-secondary mt-3 px-3 py-1.5 text-sm">
            {query.isFetching ? 'Retrying…' : 'Retry'}
          </button>
        }
      />
    )
  }
  if (!data.installed) return notInstalled
  return <><RefreshFailedNotice queries={[query]} className="px-5 pt-3 xl:px-7" />{children(data)}</>
}
