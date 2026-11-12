import { useEffect, useState, type ComponentType } from 'react'
import { useNavigate } from 'react-router-dom'
import { clsx } from 'clsx'
import { GitBranch, History, Link2, Package, RefreshCw, X } from 'lucide-react'
import { FactGrid, FactRow, FactValue, FetchResult, type Fact } from '@skyhook-io/k8s-ui'
import { isStableClientError, useObservedHelmRelease } from '../../api/client'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { getHelmStatusColor, SEVERITY_BADGE } from '../../utils/badge-colors'
import type { NavigateToResource } from '../../utils/navigation'
import type { FluxHelmRevision, ObservedHelmReleaseDetail, SelectedHelmRelease } from '../../types'
import { Tooltip } from '../ui/Tooltip'
import { formatDate } from './helm-utils'
import { ObservedHelmDrawerNote } from './ObservedHelmNotice'
import { OwnedResources } from './OwnedResources'
import { ReleaseControllerNote, releaseControllerOf } from './releaseController'

type TabId = 'overview' | 'history' | 'resources'

// Only what can hold data here: Helm's own tabs would all say the same thing,
// and the note above says it once. History is what Flux recorded, if anything.
const TABS: { id: TabId; label: string; icon: ComponentType<{ className?: string }> }[] = [
  { id: 'overview', label: 'Overview', icon: Package },
  { id: 'history', label: 'History', icon: History },
  { id: 'resources', label: 'Resources', icon: Link2 },
]

const UNREAD = 'Helm keeps this in its release Secret, which Radar can’t read here.'
const unknownFact = (source: string): Fact => ({ text: 'Unknown', tone: 'unknown', detail: source })
// found lists the values when objects disagree, as an interrupted upgrade
// leaves them; showing them is more use than "Unknown".
const stampedFact = (value: string | undefined, label: string, whenMissing: string, found?: string[]): Fact => {
  if (value) return { text: value, tone: 'neutral', source: `From ${label}` }
  if (found?.length) {
    return { text: `Mixed: ${found.join(', ')}`, tone: 'neutral', source: `From ${label}`, detail: 'Its objects carry different values, as an interrupted upgrade can leave them.' }
  }
  return unknownFact(whenMissing)
}

interface ObservedHelmReleaseDrawerProps {
  release: SelectedHelmRelease
  onClose: () => void
  onNavigateToResource?: NavigateToResource
}

/**
 * A release as its deployed objects describe it, for someone who can't read
 * Helm's storage. It issues no read of Helm's release routes, and what only
 * those routes know stays visibly unknown.
 */
export function ObservedHelmReleaseDrawer({ release, onClose, onNavigateToResource }: ObservedHelmReleaseDrawerProps) {
  const navigate = useNavigate()
  const [activeTab, setActiveTab] = useState<TabId>('overview')
  const { data, isLoading, error, refetch: refetchRelease } = useObservedHelmRelease(release.namespace, release.name)
  const [refetch, isRefreshAnimating] = useRefreshAnimation(refetchRelease)
  useEffect(() => setActiveTab('overview'), [release.namespace, release.name])

  const flux = data?.managedByFluxHelmRelease
  const controller = data ? releaseControllerOf(data) : undefined
  const otherController = controller?.tool !== 'flux' ? controller : undefined
  const tabs = TABS.filter((tab) => tab.id !== 'history' || data?.fluxHistory?.length)
  const shownTab = tabs.some((tab) => tab.id === activeTab) ? activeTab : 'overview'

  return (
    <>
      <div className="border-b border-theme-border shrink-0">
        <div className="flex items-center justify-between px-4 pt-3 pb-2">
          <div className="flex items-center gap-2 flex-wrap">
            <span className={clsx('badge', SEVERITY_BADGE.info)}>Helm Release</span>
            <Tooltip content={UNREAD}>
              <span className={clsx('badge', SEVERITY_BADGE.neutral)}>Status unknown</span>
            </Tooltip>
          </div>
          <div className="flex items-center gap-1">
            <Tooltip content="Refresh">
              <button
                onClick={refetch}
                disabled={isRefreshAnimating}
                className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-50 disabled:pointer-events-none"
              >
                <RefreshCw className={clsx('w-4 h-4', isRefreshAnimating && 'animate-spin')} />
              </button>
            </Tooltip>
            <Tooltip content="Close (Esc)">
              <button onClick={onClose} className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded">
                <X className="w-4 h-4" />
              </button>
            </Tooltip>
          </div>
        </div>

        <div className="px-4 pb-3">
          <div className="flex items-center gap-2">
            <Package className="w-5 h-5 text-purple-400" />
            <h2 className="text-lg font-semibold text-theme-text-primary truncate">{release.name}</h2>
          </div>
          <p className="text-sm text-theme-text-tertiary">{release.namespace}</p>
          {flux && (
            <Tooltip content={`Installed by Flux helm-controller via HelmRelease ${flux}.`} wrapperClassName="mt-1">
              <button
                type="button"
                onClick={() => {
                  const [ns, name] = flux.split('/')
                  navigate(`/gitops/detail/helmreleases/${encodeURIComponent(ns || '_')}/${encodeURIComponent(name)}`)
                }}
                className="inline-flex items-center gap-1 rounded border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-700 dark:text-amber-400 hover:bg-amber-500/20 transition-colors"
              >
                <GitBranch className="w-3 h-3" />
                Managed by Flux · {flux}
              </button>
            </Tooltip>
          )}
          {otherController && <ReleaseControllerNote observed controller={otherController} onNavigateToResource={onNavigateToResource} />}
        </div>

        <div className="flex items-center gap-1 px-4 pb-2 overflow-x-auto">
          {tabs.map((tab) => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={clsx(
                'flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md transition-colors whitespace-nowrap',
                shownTab === tab.id
                  ? 'bg-theme-elevated text-theme-text-primary'
                  : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated/50'
              )}
            >
              <tab.icon className="w-3.5 h-3.5" />
              {tab.label}
            </button>
          ))}
        </div>
      </div>

      <div className="flex-1 overflow-y-auto">
        <ObservedHelmDrawerNote coverage={data?.objectCoverage} refreshFailed={Boolean(data && error && !isStableClientError(error))} />
        {!data || isStableClientError(error) ? (
          <FetchResult loading={isLoading} error={error} notFoundMessage="No release found on the objects you can read" className="h-32" />
        ) : shownTab === 'overview' ? (
          <OverviewTab release={data} />
        ) : shownTab === 'history' ? (
          <FluxHistory release={data} />
        ) : (
          <OwnedResources resources={data.resources ?? []} onNavigate={onNavigateToResource} summaryLabel="with this release’s Helm metadata" />
        )}
      </div>
    </>
  )
}

function OverviewTab({ release }: { release: ObservedHelmReleaseDetail }) {
  const health = release.resourceHealth
  const lastRecord = release.fluxHistory?.[0]
  const chartWhenMissing = release.otherCharts?.length
    ? `Its objects name several charts, and none matches the release name: ${release.otherCharts.join(', ')}.`
    : 'Its objects have no helm.sh/chart label.'
  // When Flux recorded the release, these three values are Flux's, not labels.
  const fact = (value: string | undefined, label: string, whenMissing: string, found?: string[]): Fact =>
    lastRecord?.chartName ? (value ? { text: value, tone: 'neutral', source: 'Recorded by Flux' } : unknownFact('Flux didn’t record this.')) : stampedFact(value, label, whenMissing, found)
  return (
    <div className="p-4 space-y-4">
      <div className="bg-theme-elevated/30 rounded-lg p-4">
        <FactGrid>
          <FactRow label="Chart"><FactValue fact={fact(release.chart, 'helm.sh/chart', chartWhenMissing)} /></FactRow>
          <FactRow label="Chart version"><FactValue fact={fact(release.chartVersion, 'helm.sh/chart', 'No version could be read from its objects.', release.chartVersions)} /></FactRow>
          <FactRow label="App version"><FactValue fact={fact(release.appVersion, 'app.kubernetes.io/version', 'No app version could be read from its objects.', release.appVersions)} /></FactRow>
          {release.otherCharts && release.otherCharts.length > 0 && (
            <FactRow label={release.chart ? 'Other charts' : 'Charts found'}>
              <span className="text-theme-text-secondary">{release.otherCharts.join(', ')}</span>
            </FactRow>
          )}
          <FactRow label="Objects">
            <FactValue fact={{ text: `${release.resourceCount} you can read`, tone: 'neutral', detail: 'The release may have more. Secrets and kinds Radar isn’t watching aren’t counted.' }} />
          </FactRow>
          <FactRow label="Object health">
            <FactValue fact={health && health !== 'unknown'
              ? { text: release.healthSummary ? `${health}, ${release.healthSummary}` : health, tone: health === 'healthy' ? 'healthy' : health === 'degraded' ? 'degraded' : health === 'unhealthy' ? 'unhealthy' : 'unknown', detail: 'From the objects you can read, not from Helm.' }
              : unknownFact('Radar can’t tell from the objects you can read.')} />
          </FactRow>
          <FactRow label="Status"><FactValue fact={unknownFact(UNREAD)} /></FactRow>
          {lastRecord && (
            <FactRow label="Flux’s last record">
              <FactValue fact={{ text: `${lastRecord.status ?? 'No status'}, revision ${lastRecord.revision}`, tone: 'neutral', at: lastRecord.lastDeployed, detail: 'Recorded by Flux, which may not match Helm now.' }} />
            </FactRow>
          )}
          <FactRow label="Revision"><FactValue fact={unknownFact(UNREAD)} /></FactRow>
          <FactRow label="Last updated"><FactValue fact={unknownFact(UNREAD)} /></FactRow>
        </FactGrid>
      </div>
    </div>
  )
}

function FluxHistory({ release }: { release: ObservedHelmReleaseDetail }) {
  const history = release.fluxHistory ?? []
  return (
    <div className="p-4 space-y-2">
      <p className="text-sm text-theme-text-secondary">
        As recorded by Flux in HelmRelease {release.managedByFluxHelmRelease}, not read from Helm.
      </p>
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-theme-text-tertiary">
            <th className="py-1.5 pr-3 font-normal">Revision</th>
            <th className="py-1.5 pr-3 font-normal">Status</th>
            <th className="py-1.5 pr-3 font-normal">Chart</th>
            <th className="py-1.5 pr-3 font-normal">App version</th>
            <th className="py-1.5 pr-3 font-normal">Last deployed</th>
            <th className="py-1.5 font-normal">Release digest</th>
          </tr>
        </thead>
        <tbody className="table-divide-subtle">
          {history.map((entry) => <FluxHistoryRow key={entry.revision} entry={entry} />)}
        </tbody>
      </table>
    </div>
  )
}

function FluxHistoryRow({ entry }: { entry: FluxHelmRevision }) {
  return (
    <tr className="text-theme-text-secondary">
      <td className="py-1.5 pr-3">{entry.revision}</td>
      <td className="py-1.5 pr-3">{entry.status ? <span className={clsx('badge', getHelmStatusColor(entry.status))}>{entry.status}</span> : '—'}</td>
      <td className="py-1.5 pr-3">{[entry.chartName, entry.chartVersion].filter(Boolean).join('-') || '—'}</td>
      <td className="py-1.5 pr-3">{entry.appVersion || '—'}</td>
      <td className="py-1.5 pr-3">{entry.lastDeployed ? formatDate(entry.lastDeployed) : '—'}</td>
      <td className="py-1.5 font-mono text-xs">
        {entry.digest ? <Tooltip content={entry.digest}><span>{entry.digest.replace('sha256:', '').slice(0, 12)}</span></Tooltip> : '—'}
      </td>
    </tr>
  )
}
