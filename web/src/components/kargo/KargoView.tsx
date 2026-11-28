import type { ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import type { UseQueryResult } from '@tanstack/react-query'
import { Workflow } from 'lucide-react'
import {
  PaneLoader,
  RadarUpgradeAction,
  SelectMenu,
  getRadarUpgradeRequirement,
  radarUpgradeDetail,
  radarUpgradeHeadline,
} from '@skyhook-io/k8s-ui'
import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'
import { useAPIResources } from '../../api/apiResources'
import { useKargoProject, useKargoWorkspace, type KargoWorkspaceResponse } from '../../api/kargo'
import { useConnection } from '../../context/ConnectionContext'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { useCNPGSidebarWorkspace } from '../cnpg/useCNPGSidebarWorkspace'
import { useVeleroSidebarWorkspace } from '../velero/useVeleroSidebarWorkspace'
import { WorkspaceFrame } from '../workspace/WorkspaceFrame'
import { Notice, RefreshFailedNotice, ScreenEmptyState } from '../workspace/layout'
import { useMergedSidebarWorkspaces } from '../workspace/sidebar'
import { useWorkspaceDrawer } from '../workspace/useWorkspaceDrawer'
import { useKargoSidebarWorkspace } from './useKargoSidebarWorkspace'
import { KARGO_PROJECT_PARAM, KARGO_STAGE_PARAM, KARGO_TITLE, chooseKargoProject, kargoDiscovered } from './routes'
import { KargoProject } from './KargoProject'

/**
 * Kargo's pipelines, one project at a time: each stage in pipeline order with
 * the version it holds, its health, verification and last promotion, and why
 * the newest version isn't there yet. Lives inside Resources with the same
 * sidebar. The project is picked here; the namespace picker does not apply.
 */
export function KargoView({ namespaces, selectedResource, onOpenResource, onCloseResource }: WorkspaceScreenProps) {
  const compactNavigation = useMediaQuery('(max-width: 1100px)')
  const { data: apiResources, isPending: discoveryPending, isError: discoveryFailed } = useAPIResources()
  // Discovery that could not be read does not say Kargo is absent; the server is asked then.
  const discovered = kargoDiscovered(apiResources) || discoveryFailed
  const workspace = useKargoWorkspace({ enabled: discovered })
  const kargoSidebar = useKargoSidebarWorkspace({ apiResources, active: true })
  const cnpgSidebar = useCNPGSidebarWorkspace({ apiResources, namespaces })
  const veleroSidebar = useVeleroSidebarWorkspace({ apiResources })
  const sidebarWorkspace = useMergedSidebarWorkspaces(cnpgSidebar, veleroSidebar, kargoSidebar)
  const { drawerTarget, inspect } = useWorkspaceDrawer({ selectedResource, onOpenResource, onCloseResource })

  return (
    <WorkspaceFrame namespaces={namespaces} categoryWorkspaces={sidebarWorkspace} compactNavigation={compactNavigation}>
      <KargoScreenGate query={workspace} discoveryPending={discoveryPending} discovered={discovered}>
        {(data) => <KargoProjects data={data} viewNamespaces={namespaces} onInspect={inspect} inspected={drawerTarget} />}
      </KargoScreenGate>
    </WorkspaceFrame>
  )
}

function KargoProjects({ data, viewNamespaces, onInspect, inspected }: {
  data: KargoWorkspaceResponse
  viewNamespaces: string[]
  onInspect: Parameters<typeof KargoProject>[0]['onInspect']
  inspected: Parameters<typeof KargoProject>[0]['inspected']
}) {
  const [params, setParams] = useSearchParams()
  const location = useLocation()
  const { project, missing } = chooseKargoProject(data.projects, params.get(KARGO_PROJECT_PARAM), viewNamespaces)
  const query = useKargoProject(project)

  if (!project) {
    const stages = data.coverage.stages?.state
    if (stages === 'uncached') {
      return <ScreenEmptyState icon={Workflow} title="Radar can see Kargo but isn’t allowed to read it" detail="In the Radar Helm chart, set rbac.crdGroups.kargo to true and upgrade." />
    }
    if (stages === 'denied') {
      return <ScreenEmptyState icon={Workflow} title="You can’t read Kargo stages" detail="Ask a cluster admin for list access to stages.kargo.akuity.io in your Kargo projects." />
    }
    if (stages === 'syncing') return <PaneLoader label="Loading Kargo…" className="flex-1" />
    return (
      <ScreenEmptyState
        icon={Workflow}
        title="No Kargo projects to show"
        detail={stages === 'full' ? 'Kargo is installed, but there are no projects with stages yet.' : 'No projects with stages were found in the namespaces Radar could read.'}
      />
    )
  }

  const select = (name: string) => {
    const next = new URLSearchParams(params)
    next.set(KARGO_PROJECT_PARAM, name)
    next.delete('drawer')
    next.delete(KARGO_STAGE_PARAM)
    setParams(next, { replace: false, state: location.state })
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-theme-border px-5 py-3 xl:px-7">
        <h1 className="text-base font-semibold text-theme-text-primary">{KARGO_TITLE}</h1>
        <div className="flex items-center gap-2 text-sm text-theme-text-secondary">
          <span id="kargo-project-label">Project</span>
          <SelectMenu
            value={project}
            options={data.projects.map((p) => ({
              value: p.name,
              label: p.name,
              description: `${p.stages} ${p.stages === 1 ? 'stage' : 'stages'}${p.problems ? `, ${p.problems} ${p.problems === 1 ? 'problem' : 'problems'}` : ''}`,
            }))}
            onChange={select}
            ariaLabel="Kargo project"
            searchPlaceholder="Find a project"
            className="min-w-[12rem]"
          />
        </div>
      </div>
      <KargoProject
        query={query}
        onInspect={onInspect}
        inspected={inspected}
        notice={missing && <Notice>Can’t find project {missing}, or you can’t read it. Showing {project}.</Notice>}
      />
    </div>
  )
}

/** Loading, needs-newer-Radar, error and not-installed states. Renders the screen once there is data. */
function KargoScreenGate({ query, discoveryPending, discovered, children }: {
  query: Pick<UseQueryResult<KargoWorkspaceResponse>, 'data' | 'error' | 'isLoading' | 'isFetching' | 'refetch' | 'isRefetchError' | 'dataUpdatedAt'>
  discoveryPending: boolean
  discovered: boolean
  children: (data: KargoWorkspaceResponse) => ReactNode
}) {
  const { connection } = useConnection()
  const data = query.data
  const notInstalled = (
    <ScreenEmptyState icon={Workflow} title="Kargo is not installed" detail={`No kargo.akuity.io resources are served in ${connection.context || 'this cluster'}.`} />
  )
  if (!data && (query.isLoading || discoveryPending)) return <PaneLoader label="Loading Kargo…" className="flex-1" />
  if (!data && !discovered) return notInstalled
  if (!data) {
    const upgrade = getRadarUpgradeRequirement(query.error)
    if (upgrade) {
      return (
        <ScreenEmptyState
          icon={Workflow}
          title={radarUpgradeHeadline('Kargo')}
          detail={radarUpgradeDetail(upgrade)}
          action={<div className="mt-3 text-sm"><RadarUpgradeAction requirement={upgrade} /></div>}
        />
      )
    }
    return (
      <ScreenEmptyState
        icon={Workflow}
        title="Kargo data unavailable"
        detail={query.error instanceof Error ? query.error.message : 'Radar could not load the Kargo data.'}
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
