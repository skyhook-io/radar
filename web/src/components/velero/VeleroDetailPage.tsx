import { useCallback, useEffect, useMemo } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useLocation, useSearchParams } from 'react-router-dom'
import { Archive, CalendarClock, History, Unplug } from 'lucide-react'
import type { WorkloadExtraTab } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { useContexts } from '../../api/client'
import { useContextSwitchFlow } from '../useContextSwitchFlow'
import { WorkloadView } from '../workload/WorkloadView'
import { ScreenEmptyState } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { currentPageLabel } from '../../utils/page-links'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { VELERO_DETAIL_KINDS, veleroDetailKindFor, veleroDetailPath, type VeleroDetailTarget, VELERO_GROUP } from './routes'
import { VeleroBackupRestores, VeleroBackupRestoresMark, VeleroLocationBackups, VeleroLocationBackupsMark, VeleroScheduleRuns, VeleroScheduleRunsMark } from './VeleroPages'

interface ReturnState {
  returnLabel?: string
  returnCtx?: string
}

/**
 * A Velero object's page: Radar's detail view framed by Velero, with a
 * composed Overview (from the Velero host) and tabs for what the object
 * relates to: a schedule's runs, a backup's restores, a location's backups.
 *
 * `ctx` pins the Kubernetes context; when the active context is another one,
 * the page says so rather than loading a same-named object.
 */
export function VeleroDetailPage({ target, onOpenResource }: { target: VeleroDetailTarget; onOpenResource: (resource: SelectedResource) => void }) {
  const location = useLocation()
  const navigate = useWorkspaceNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { connection } = useConnection()
  const activeContext = connection.context
  const pinnedContext = searchParams.get('ctx')
  const spec = VELERO_DETAIL_KINDS[target.plural]
  const queryClient = useQueryClient()
  // The page's own reads (activity, runs, a location's backups) beside the object.
  const refresh = useCallback(() => queryClient.refetchQueries({ type: 'active', queryKey: ['velero'] }), [queryClient])

  useEffect(() => {
    if (pinnedContext || !activeContext) return
    const params = new URLSearchParams(searchParams)
    params.set('ctx', activeContext)
    setSearchParams(params, { replace: true, state: location.state })
  }, [pinnedContext, activeContext]) // eslint-disable-line react-hooks/exhaustive-deps

  const returnState = (location.state ?? {}) as ReturnState
  const returnLabel = returnState.returnLabel && (!returnState.returnCtx || returnState.returnCtx === activeContext) ? returnState.returnLabel : null

  // A related Velero object opens on its own page; anything else in the drawer.
  const openRelated = useCallback(
    (res: SelectedResource) => {
      const plural = veleroDetailKindFor(kindToPluralWithGroup(res.kind, res.group ?? ''), res.group)
      if (plural) {
        navigate(veleroDetailPath({ plural, namespace: res.namespace, name: res.name }, activeContext), {
          state: { returnLabel: currentPageLabel(), returnCtx: activeContext } satisfies ReturnState,
        })
      } else {
        onOpenResource(res)
      }
    },
    [navigate, activeContext, onOpenResource],
  )

  const extraTabs = useMemo<WorkloadExtraTab[] | undefined>(() => {
    const { namespace, name } = target
    switch (target.plural) {
      case 'schedules':
        return [{ id: 'runs', label: 'Runs', after: 'overview', icon: <History className="h-4 w-4" />, badge: <VeleroScheduleRunsMark namespace={namespace} name={name} />, render: () => <VeleroScheduleRuns namespace={namespace} name={name} onNavigate={openRelated} /> }]
      case 'backups':
        return [{ id: 'restores', label: 'Restores', after: 'overview', icon: <History className="h-4 w-4" />, badge: <VeleroBackupRestoresMark namespace={namespace} name={name} />, render: () => <VeleroBackupRestores namespace={namespace} name={name} onNavigate={openRelated} /> }]
      case 'backupstoragelocations':
        return [{ id: 'backups', label: 'Backups', after: 'overview', icon: <Archive className="h-4 w-4" />, badge: <VeleroLocationBackupsMark namespace={namespace} name={name} />, render: () => <VeleroLocationBackups namespace={namespace} name={name} onNavigate={openRelated} /> }]
      default:
        return undefined
    }
  }, [target, openRelated])

  if (pinnedContext && activeContext && pinnedContext !== activeContext) {
    return <NotInContext target={target} pinnedContext={pinnedContext} activeContext={activeContext} />
  }

  // Where the object lives: Velero / <its kind's list> /.
  const titlePrefix = (
    <nav aria-label="Location" className="flex shrink-0 items-center gap-1.5 text-sm text-theme-text-tertiary">
      <CalendarClock className="h-3.5 w-3.5" />
      <button type="button" onClick={() => navigate('/velero')} className="hover:text-theme-text-primary hover:underline">Velero</button>
      <span>/</span>
      <button type="button" onClick={() => navigate(spec.listPath)} className="hover:text-theme-text-primary hover:underline">{spec.label}</button>
      <span>/</span>
    </nav>
  )

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <WorkloadView
        key={`${target.plural}/${target.namespace}/${target.name}`}
        kind={target.plural}
        namespace={target.namespace}
        name={target.name}
        group={VELERO_GROUP}
        expanded
        onBack={() => (returnLabel ? navigate(-1) : navigate(spec.listPath))}
        hideBackButton
        titlePrefix={titlePrefix}
        inlineBadges
        // The crumb names the kind.
        hideKindBadge
        truncateName
        onNavigateToResource={openRelated}
        onRefresh={refresh}
        extraTabs={extraTabs}
      />
    </div>
  )
}

function NotInContext({ target, pinnedContext, activeContext }: { target: VeleroDetailTarget; pinnedContext: string; activeContext: string }) {
  const navigate = useWorkspaceNavigate()
  const { data: contexts } = useContexts()
  const { requestSwitch, confirmDialog } = useContextSwitchFlow()
  const pinned = contexts?.find((c) => c.name === pinnedContext)
  return (
    <>
      <ScreenEmptyState
        icon={Unplug}
        title={`${target.name} is not in ${activeContext}`}
        detail={`This link points at ${VELERO_DETAIL_KINDS[target.plural].kind} ${target.name} in context ${pinnedContext}. Radar does not open a same-named object from another cluster.`}
        action={
          <div className="mt-4 flex flex-wrap justify-center gap-2">
            {pinned && (
              <button type="button" onClick={() => requestSwitch(pinned)} className="btn-brand px-3 py-1.5 text-sm font-medium">
                Switch back to {pinnedContext}
              </button>
            )}
            <button type="button" onClick={() => navigate('/velero')} className="btn-secondary px-3 py-1.5 text-sm">
              Go to the Velero Overview
            </button>
          </div>
        }
      />
      {confirmDialog}
    </>
  )
}
