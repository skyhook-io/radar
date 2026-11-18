import { useCallback } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'
import { useAPIResources } from '../../api/apiResources'
import { useSubstrateStatus, useSubstrateWorkspace, type SubstrateRef, type SubstrateSubject } from '../../api/substrateWorkspace'
import { useConnection } from '../../context/ConnectionContext'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { currentPageLabel } from '../../utils/page-links'
import { refToSelectedResource } from '../../utils/navigation'
import { encodeDrawerTrail } from '../../utils/drawer-trail'
import type { SelectedResource } from '../../types'
import { useCNPGSidebarWorkspace } from '../cnpg/useCNPGSidebarWorkspace'
import { useVeleroSidebarWorkspace } from '../velero/useVeleroSidebarWorkspace'
import { WorkspaceFrame } from '../workspace/WorkspaceFrame'
import { useMergedSidebarWorkspaces } from '../workspace/sidebar'
import { useWorkspaceDrawer } from '../workspace/useWorkspaceDrawer'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import {
  ACTOR_DRAWER_PARAM,
  SUBSTRATE_GROUP,
  SUBSTRATE_WORKERPOOL_KIND,
  decodeActorParam,
  encodeActorParam,
  parseSubstrateRoute,
  substrateActorPath,
  substrateActorsPath,
  substrateTemplatePath,
} from './routes'
import { SubstrateActorDrawer } from './SubstrateActorDrawer'
import { SubstrateActorPage } from './SubstrateActorPage'
import { SubstrateActorsScreen } from './SubstrateActors'
import { SubstrateOverview } from './SubstrateOverview'
import { SubstratePoolPage } from './SubstratePoolPage'
import { SubstrateTemplatePage, SubstrateTemplatesScreen } from './SubstrateTemplates'
import { SubstrateWorkersScreen } from './SubstrateWorkers'
import { SubstrateScreenGate } from './shared'
import { substrateDiscovered, useSubstrateSidebarWorkspace } from './useSubstrateSidebarWorkspace'

/**
 * One drawer at a time, in one URL update each way: opening an actor's drawer
 * closes the resource drawer, and opening a resource closes the actor's. Two
 * writes from one render each start from the same old params, so the second
 * would bring the first drawer back.
 */
export function actorDrawerParams(params: URLSearchParams, actor: SubstrateRef | null): URLSearchParams {
  const p = new URLSearchParams(params)
  if (actor) {
    p.set(ACTOR_DRAWER_PARAM, encodeActorParam(actor))
    p.delete('drawer')
  } else {
    p.delete(ACTOR_DRAWER_PARAM)
  }
  return p
}

export function resourceDrawerParams(params: URLSearchParams, resource: SelectedResource): URLSearchParams {
  const p = new URLSearchParams(params)
  p.delete(ACTOR_DRAWER_PARAM)
  p.set('drawer', encodeDrawerTrail([resource]))
  return p
}

/**
 * The Agent Substrate workspace: Overview, Actors, Templates, Workers and the
 * pages of actors, templates and WorkerPools. It lives inside Resources with
 * the same sidebar. Actors live in atespaces, so the namespace picker does
 * not apply; the screens have an Atespace filter instead.
 */
export function SubstrateView({ namespaces, selectedResource, onOpenResource, onCloseResource }: WorkspaceScreenProps) {
  const location = useLocation()
  const route = parseSubstrateRoute(location.pathname)
  const navigate = useWorkspaceNavigate()
  const [params, setParams] = useSearchParams()
  const { connection } = useConnection()
  const narrow = useMediaQuery('(max-width: 1100px)')
  const laptop = useMediaQuery('(max-width: 1300px)')
  const onPage = !!(route.actor || route.template || route.pool)
  const compactNavigation = narrow || (laptop && onPage)
  const { data: apiResources, isPending: discoveryPending, isError: discoveryFailed } = useAPIResources()
  // Discovery that could not be read does not say Substrate is absent; the server is asked then.
  const discovered = substrateDiscovered(apiResources) || discoveryFailed
  const status = useSubstrateStatus(discovered)
  const workspace = useSubstrateWorkspace(discovered)

  const substrateSidebar = useSubstrateSidebarWorkspace({ apiResources, active: route.pool ? undefined : route.screen })
  const cnpgSidebar = useCNPGSidebarWorkspace({ apiResources, namespaces })
  const veleroSidebar = useVeleroSidebarWorkspace({ apiResources })
  const sidebarWorkspace = useMergedSidebarWorkspaces(cnpgSidebar, veleroSidebar, substrateSidebar)
  const selectedKind = route.pool ? SUBSTRATE_WORKERPOOL_KIND : null
  const { drawerTarget, inspect } = useWorkspaceDrawer({ selectedResource, onOpenResource, onCloseResource })

  const openActor = decodeActorParam(params.get(ACTOR_DRAWER_PARAM))
  const setActorDrawer = useCallback(
    (actor: SubstrateRef | null) => setParams(actorDrawerParams(params, actor), { replace: true, state: location.state }),
    [params, setParams, location.state],
  )
  const inspectResource = useCallback(
    (resource: SelectedResource) => {
      if (params.has(ACTOR_DRAWER_PARAM)) setParams(resourceDrawerParams(params, resource), { replace: true, state: location.state })
      else inspect(resource)
    },
    [inspect, params, setParams, location.state],
  )
  const openFull = useCallback(
    (actor: SubstrateRef, tab?: string) => navigate(substrateActorPath(actor, connection.context || undefined, tab), { state: { returnLabel: currentPageLabel(), returnCtx: connection.context } }),
    [navigate, connection.context],
  )

  // What a link names: an actor list, an actor (its drawer, or its page from
  // an actor's page), a template's page, or a Kubernetes object's drawer.
  const openSubject = useCallback(
    (s: SubstrateSubject) => {
      switch (s.kind) {
        case 'Actors':
          navigate(substrateActorsPath(s.filter))
          return
        case 'Actor':
          if (s.atespace && s.name) {
            if (route.actor) openFull({ atespace: s.atespace, name: s.name })
            else setActorDrawer({ atespace: s.atespace, name: s.name })
          }
          return
        case 'ActorTemplate':
          if (s.atespace && s.name) navigate(substrateTemplatePath({ atespace: s.atespace, name: s.name }, connection.context || undefined))
          return
        case 'WorkerPool':
          inspectResource(refToSelectedResource({ kind: 'WorkerPool', group: SUBSTRATE_GROUP, namespace: s.namespace ?? '', name: s.name ?? '' }))
          return
        case 'Worker':
        case 'Pod':
          inspectResource(refToSelectedResource({ kind: 'Pod', group: '', namespace: s.namespace ?? '', name: s.name ?? '' }))
          return
        case 'Node':
          inspectResource(refToSelectedResource({ kind: 'Node', group: '', namespace: '', name: s.name ?? '' }))
      }
    },
    [navigate, route.actor, openFull, setActorDrawer, inspectResource, connection.context],
  )

  const screen = (() => {
    if (route.actor) return <SubstrateActorPage actor={route.actor} onOpenSubject={openSubject} />
    if (route.template) return <SubstrateTemplatePage template={route.template} onOpenSubject={openSubject} />
    if (route.pool) return <SubstratePoolPage pool={route.pool} onOpenResource={onOpenResource} />
    return (
      <SubstrateScreenGate query={workspace} discoveryPending={discoveryPending} discovered={discovered}>
        {(data) => {
          switch (route.screen) {
            case 'actors':
              return <SubstrateActorsScreen workspace={data} status={status.data} onOpenActor={setActorDrawer} openActor={openActor} />
            case 'templates':
              return <SubstrateTemplatesScreen workspace={data} status={status.data} />
            case 'workers':
              return <SubstrateWorkersScreen workspace={data} status={status.data} onOpenSubject={openSubject} />
            default:
              return <SubstrateOverview data={data} status={status.data} onInspect={inspectResource} inspected={drawerTarget} onOpenSubject={openSubject} />
          }
        }}
      </SubstrateScreenGate>
    )
  })()

  return (
    <WorkspaceFrame namespaces={namespaces} categoryWorkspaces={sidebarWorkspace} selectedKind={selectedKind} compactNavigation={compactNavigation}>
      {screen}
      <SubstrateActorDrawer actor={route.actor ? null : openActor} onClose={() => setActorDrawer(null)} onOpenFull={openFull} onOpenSubject={openSubject} />
    </WorkspaceFrame>
  )
}
