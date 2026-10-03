import { useCallback, useEffect, useMemo } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { Activity, ArrowLeft, Database, Gauge, ShieldCheck, Unplug } from 'lucide-react'
import type { WorkloadExtraTab } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { useContexts } from '../../api/client'
import { useContextSwitchFlow } from '../useContextSwitchFlow'
import { WorkloadView } from '../workload/WorkloadView'
import { CNPGClusterActivity } from './CNPGClusterActivity'
import { CNPGClusterRuntime } from './CNPGClusterRuntime'
import { CNPGProtection } from './CNPGProtection'
import { CNPGRestoreValidation } from './recovery/CNPGRestoreValidation'
import { CNPGScreenGate } from './shared'
import { CNPG_DETAIL_KINDS, CNPG_SCREENS, cnpgDetailKindFor, cnpgDetailPath, cnpgScreenPath, type CNPGDetailTarget } from './routes'
import { currentPageLabel } from '../../utils/page-links'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'
import { CNPGOperatorBanner } from './CNPGOperatorBanner'
import { ScreenEmptyState } from '../workspace/layout'

interface ReturnState {
  returnLabel?: string
  returnCtx?: string
}

function ClusterProtectionTab({ namespace, name, onInspect }: { namespace: string; name: string; onInspect: (r: SelectedResource) => void }) {
  const { query, fleet } = useCNPGFleet([namespace])
  const [searchParams] = useSearchParams()
  return (
    <CNPGScreenGate query={query} fleet={fleet}>
      {(data, readyFleet) => (
        <div className="flex min-h-0 flex-1 flex-col">
          {readyFleet.rows.find((r) => r.namespace === namespace && r.name === name)?.cluster?.spec?.bootstrap?.recovery && (
            <CNPGRestoreValidation namespace={namespace} name={name} />
          )}
          <CNPGProtection
            data={data}
            fleet={readyFleet}
            namespaces={[namespace]}
            searchParams={searchParams}
            onSetParams={() => {}}
            onInspect={onInspect}
            inspected={null}
            onClearNamespaces={() => {}}
            scopeCluster={{ namespace, name }}
          />
        </div>
      )}
    </CNPGScreenGate>
  )
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
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { connection } = useConnection()
  const activeContext = connection.context
  const pinnedContext = searchParams.get('ctx')

  // A link opened without a context belongs to the one active now; pin it so a
  // later context switch shows "not in this context" instead of a same-named
  // object from the other cluster.
  useEffect(() => {
    if (pinnedContext || !activeContext) return
    const params = new URLSearchParams(searchParams)
    params.set('ctx', activeContext)
    setSearchParams(params, { replace: true, state: location.state })
  }, [pinnedContext, activeContext]) // eslint-disable-line react-hooks/exhaustive-deps
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

  const extraTabs = useMemo<WorkloadExtraTab[] | undefined>(() => {
    if (target.plural !== 'clusters') return undefined
    return [
      {
        id: 'runtime',
        label: 'Runtime',
        icon: <Gauge className="h-4 w-4" />,
        after: 'spec',
        render: () => (
          <CNPGClusterRuntime
            namespace={target.namespace}
            name={target.name}
            onOpenLogs={(pod) => setSearchParams(new URLSearchParams({ ...Object.fromEntries(searchParams), tab: 'logs', pod }), { replace: true, state: location.state })}
            onOpenInterval={(tab, since, until) => {
              const params = new URLSearchParams({ ...Object.fromEntries(searchParams), tab, since, until })
              params.delete('pod')
              setSearchParams(params, { state: location.state })
            }}
          />
        ),
      },
      {
        id: 'protection',
        label: 'Protection',
        icon: <ShieldCheck className="h-4 w-4" />,
        after: 'spec',
        render: () => <ClusterProtectionTab namespace={target.namespace} name={target.name} onInspect={onOpenResource} />,
      },
      {
        id: 'activity',
        label: 'Activity',
        icon: <Activity className="h-4 w-4" />,
        replaces: 'timeline',
        render: () => (
          <CNPGClusterActivity namespace={target.namespace} name={target.name} onNavigate={openRelated} />
        ),
      },
    ]
  }, [target.plural, target.namespace, target.name, onOpenResource, openRelated, searchParams, setSearchParams, location.state])

  if (pinnedContext && activeContext && pinnedContext !== activeContext) {
    return <NotInContext target={target} pinnedContext={pinnedContext} activeContext={activeContext} homeLabel={home.label} homePath={home.path} />
  }

  const outsideFilter = namespaces.length > 0 && !!target.namespace && !namespaces.includes(target.namespace)

  const breadcrumb = (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
      {returnLabel && (
        <>
          <button
            type="button"
            onClick={() => navigate(-1)}
            aria-label={`Return to ${returnLabel}`}
            className="inline-flex items-center gap-1 rounded-md py-0.5 pr-2 text-theme-text-secondary hover:text-theme-text-primary"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            {returnLabel}
          </button>
          <span className="h-4 w-px bg-theme-border" aria-hidden />
        </>
      )}
      <nav aria-label="Location" className="flex min-w-0 items-center gap-1.5 text-theme-text-tertiary">
        <Database className="h-3.5 w-3.5" />
        <button type="button" onClick={() => navigate(cnpgScreenPath('overview'))} className="hover:text-theme-text-primary hover:underline">
          CloudNativePG
        </button>
        {home.id !== 'overview' && (
          <>
            <span>/</span>
            <button type="button" onClick={() => navigate(home.path)} className="hover:text-theme-text-primary hover:underline">
              {home.label}
            </button>
          </>
        )}
        <span>/</span>
        <span className="truncate text-theme-text-secondary">{target.name}</span>
      </nav>
      {outsideFilter && (
        <span className="text-xs text-theme-text-tertiary">
          Namespace {target.namespace} is outside your namespace filter; this object stays open.
        </span>
      )}
      {target.plural === 'clusters' && <CNPGOperatorBanner namespaces={[target.namespace]} className="mt-1 w-full" />}
    </div>
  )

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <WorkloadView
        key={`${target.plural}/${target.namespace}/${target.name}`}
        kind={target.plural}
        namespace={target.namespace}
        name={target.name}
        group={target.group}
        expanded
        onBack={() => (returnLabel ? navigate(-1) : navigate(home.path))}
        hideBackButton
        breadcrumb={breadcrumb}
        onNavigateToResource={openRelated}
        extraTabs={extraTabs}
      />
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
  const navigate = useNavigate()
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
              Go to {homeLabel === 'Overview' ? 'CloudNativePG Overview' : homeLabel}
            </button>
          </div>
        }
      />
      {confirmDialog}
    </>
  )
}
