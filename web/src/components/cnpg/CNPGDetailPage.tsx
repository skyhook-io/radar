import { usePinnedDetailContext } from '../workspace/detailContext'
import { useCallback, useMemo } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { Activity, ArrowLeft, Database, Gauge, HardDrive, Network, Settings2, ShieldCheck, Unplug } from 'lucide-react'
import { refToSelectedResource, Tooltip, type WorkloadExtraTab } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useContexts, useRadarFeature } from '../../api/client'
import { useContextSwitchFlow } from '../useContextSwitchFlow'
import { WorkloadView } from '../workload/WorkloadView'
import { CNPGClusterActivity } from './CNPGClusterActivity'
import { CNPGPerformance } from './CNPGPerformance'
import { CNPGReplicationTab } from './CNPGReplicationTab'
import { CNPGBackupsTab, CNPGClusterHeaderChips, CNPGConfigurationLead, CNPGStorageTab } from './CNPGClusterTabs'
import type { CNPGChartGroup } from './CNPGTrends'
import { CNPG_DETAIL_KINDS, CNPG_SCREENS, cnpgDetailKindFor, cnpgDetailPath, cnpgScreenPath, cnpgViewHoldsOnlyKind, type CNPGDetailTarget } from './routes'
import { CNPG_CLUSTER_TAB_ORDER, cnpgDimensionTab } from './paths'
import { currentPageLabel } from '../../utils/page-links'
import { CNPGOperatorBanner } from './CNPGOperatorBanner'
import { ScreenEmptyState } from '../workspace/layout'
import { useCNPGNavigate } from './useCNPGNavigate'

interface ReturnState {
  returnLabel?: string
  returnCtx?: string
}

/**
 * The full detail of a CloudNativePG object, framed by the workspace: the
 * Resources sidebar keeps the workspace destination highlighted, a return
 * control goes back to the task the user came from, and the crumb names the
 * object's place. The object's own sections come from Radar's detail view.
 *
 * `ctx` in the URL pins the Kubernetes context; when the active context is a
 * different one, the page says so instead of loading a same-named object.
 */
export function CNPGDetailPage({
  target,
  namespaces,
  onOpenResource,
}: {
  target: CNPGDetailTarget
  namespaces: string[]
  onOpenResource: (resource: SelectedResource) => void
}) {
  const location = useLocation()
  const navigate = useCNPGNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { activeContext, pinnedContext } = usePinnedDetailContext()
  const returnState = (location.state ?? {}) as ReturnState
  const spec = CNPG_DETAIL_KINDS[target.plural]
  const home = CNPG_SCREENS.find((s) => s.id === spec.home)!
  const returnLabel = returnState.returnLabel && (!returnState.returnCtx || returnState.returnCtx === activeContext) ? returnState.returnLabel : null

  const openRelated = useCallback(
    (res: SelectedResource) => {
      const plural = cnpgDetailKindFor(res.kind, res.group)
      if (plural) {
        navigate(cnpgDetailPath({ plural, namespace: res.namespace, name: res.name }, activeContext), {
          state: { returnLabel: currentPageLabel(), returnCtx: activeContext } satisfies ReturnState,
        })
      } else {
        onOpenResource(res)
      }
    },
    [navigate, activeContext, onOpenResource],
  )

  // A Radar without the workspace endpoints shows the standard detail.
  const cnpgWorkspace = useRadarFeature('cnpgWorkspace').support !== 'unsupported'
  const isCluster = target.plural === 'clusters' && cnpgWorkspace
  // A tab change on this page, applied like a tab click: the previous tab's
  // own params (section, chart group, instance) are dropped unless named.
  const goTab = useCallback(
    (tab: string, extra: Record<string, string> = {}) => {
      const params = new URLSearchParams(searchParams)
      for (const k of ['section', 'charts', 'instance', 'pod', 'container', 'since', 'until']) params.delete(k)
      params.set('tab', tab)
      for (const [k, v] of Object.entries(extra)) params.set(k, v)
      setSearchParams(params, { replace: true, state: location.state })
    },
    [searchParams, setSearchParams, location.state],
  )
  const openHistory = useCallback((charts: CNPGChartGroup) => goTab('performance', { section: 'history', charts }), [goTab])
  const extraTabs = useMemo<WorkloadExtraTab[] | undefined>(() => {
    if (!isCluster) return undefined
    const ns = target.namespace
    const name = target.name
    return [
      {
        id: 'replication',
        label: 'Replication',
        icon: <Network className="h-4 w-4" />,
        render: () => (
          <CNPGReplicationTab
            namespace={ns}
            name={name}
            onOpenLogs={(pod) => goTab('logs', { pod })}
            onOpenHistory={() => openHistory('replication')}
            onNavigate={(ref) => openRelated(refToSelectedResource(ref))}
          />
        ),
      },
      {
        id: 'storage',
        label: 'Storage',
        icon: <HardDrive className="h-4 w-4" />,
        render: () => <CNPGStorageTab namespace={ns} name={name} onOpenHistory={() => openHistory('storage')} onOpenReplication={() => goTab('replication')} />,
      },
      {
        id: 'performance',
        label: 'Performance',
        icon: <Gauge className="h-4 w-4" />,
        render: () => (
          <CNPGPerformance
            namespace={ns}
            name={name}
            onOpenInterval={(tab, since, until) => {
              const params = new URLSearchParams(searchParams)
              params.delete('pod')
              params.set('tab', tab)
              params.set('since', since)
              params.set('until', until)
              setSearchParams(params, { state: location.state })
            }}
          />
        ),
      },
      {
        id: 'backups',
        label: 'Backups',
        icon: <ShieldCheck className="h-4 w-4" />,
        render: () => (
          <CNPGBackupsTab
            namespace={ns}
            name={name}
            onInspect={onOpenResource}
            onOpenLogs={(pod, container) => goTab('logs', { pod, container })}
            onOpenOperator={() => navigate(cnpgScreenPath('operator'), { state: { returnLabel: currentPageLabel(), returnCtx: activeContext } satisfies ReturnState })}
          />
        ),
      },
      {
        id: 'activity',
        label: 'Activity',
        icon: <Activity className="h-4 w-4" />,
        replaces: 'timeline',
        render: () => <CNPGClusterActivity namespace={ns} name={name} onNavigate={openRelated} />,
      },
    ]
  }, [isCluster, target.namespace, target.name, goTab, openHistory, onOpenResource, openRelated, navigate, activeContext, searchParams, setSearchParams, location.state])

  if (pinnedContext && activeContext && pinnedContext !== activeContext) {
    return <NotInContext target={target} pinnedContext={pinnedContext} activeContext={activeContext} homeLabel={home.label} homePath={home.path} />
  }

  const outsideFilter = namespaces.length > 0 && !!target.namespace && !namespaces.includes(target.namespace)

  // Where the object lives, on its title line: the return to the previous
  // page (drill-downs only), then its place in the workspace.
  const titlePrefix = (
    <div className="flex min-w-0 items-center gap-x-2 text-sm">
      {returnLabel && (
        <>
          <Tooltip content={`Return to ${returnLabel}`} position="bottom">
            <button
              type="button"
              onClick={() => navigate(-1)}
              aria-label={`Return to ${returnLabel}`}
              className="inline-flex max-w-[12rem] items-center gap-1 rounded-md py-0.5 pr-1 text-theme-text-secondary hover:text-theme-text-primary"
            >
              <ArrowLeft className="h-3.5 w-3.5 shrink-0" />
              <span className="truncate">{returnLabel}</span>
            </button>
          </Tooltip>
          <span className="h-4 w-px shrink-0 bg-theme-border" aria-hidden />
        </>
      )}
      {/* Workspace / view / — the view is the place to go back to; the
          workspace name is plain text, as on the views' own titles. */}
      <nav aria-label="Location" className="flex shrink-0 items-center gap-1.5 text-theme-text-tertiary">
        <Database className="h-3.5 w-3.5" />
        <span>CloudNativePG</span>
        <span>/</span>
        <button type="button" onClick={() => navigate(home.path)} className="hover:text-theme-text-primary hover:underline">
          {home.label}
        </button>
        <span>/</span>
      </nav>
    </div>
  )
  const namespaceNote = outsideFilter ? (
    <Tooltip content="Your namespace filter leaves this namespace out; an object you open stays open." position="bottom">
      <span className="whitespace-nowrap text-xs text-theme-text-tertiary">· outside your namespace filter</span>
    </Tooltip>
  ) : undefined

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      {/* Above the title line, before any status the page shows. */}
      {target.plural === 'clusters' && <CNPGOperatorBanner namespaces={[target.namespace]} className="mx-6 mt-3 shrink-0" />}
      <div className="flex min-h-0 flex-1 flex-col">
        <WorkloadView
          key={`${target.plural}/${target.namespace}/${target.name}`}
          kind={target.plural}
          namespace={target.namespace}
          name={target.name}
          group={target.group}
          expanded
          onBack={() => (returnLabel ? navigate(-1) : navigate(home.path))}
          hideBackButton
          titlePrefix={titlePrefix}
          inlineBadges
          namespaceNote={namespaceNote}
          hideKindBadge={cnpgViewHoldsOnlyKind(target.plural)}
          onNavigateToResource={openRelated}
          extraTabs={extraTabs}
          tabOrder={isCluster ? CNPG_CLUSTER_TAB_ORDER : undefined}
          subheader={isCluster ? <CNPGClusterHeaderChips namespace={target.namespace} name={target.name} onSelect={(id) => goTab(cnpgDimensionTab(id))} /> : undefined}
          specTab={
            isCluster
              ? { label: 'Configuration', icon: <Settings2 className="h-4 w-4" />, lead: <CNPGConfigurationLead namespace={target.namespace} name={target.name} onNavigate={openRelated} /> }
              : undefined
          }
        />
      </div>
    </div>
  )
}

function NotInContext({
  target,
  pinnedContext,
  activeContext,
  homeLabel,
  homePath,
}: {
  target: CNPGDetailTarget
  pinnedContext: string
  activeContext: string
  homeLabel: string
  homePath: string
}) {
  const navigate = useCNPGNavigate()
  const { data: contexts } = useContexts()
  const { requestSwitch, confirmDialog } = useContextSwitchFlow()
  const pinned = contexts?.find((c) => c.name === pinnedContext)
  return (
    <>
      <ScreenEmptyState
        icon={Unplug}
        title={`${target.name} is not in ${activeContext}`}
        detail={`This link points at ${CNPG_DETAIL_KINDS[target.plural].kind} ${target.name} in context ${pinnedContext}. Radar does not open a same-named object from another cluster.`}
        action={
          <div className="mt-4 flex flex-wrap justify-center gap-2">
            {pinned && (
              <button type="button" onClick={() => requestSwitch(pinned)} className="btn-brand px-3 py-1.5 text-sm font-medium">
                Switch back to {pinnedContext}
              </button>
            )}
            <button
              type="button"
              onClick={() => navigate(homePath)}
              className="btn-secondary px-3 py-1.5 text-sm"
            >
              Go to CloudNativePG {homeLabel}
            </button>
          </div>
        }
      />
      {confirmDialog}
    </>
  )
}
