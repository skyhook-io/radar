import { useLocation } from 'react-router-dom'
import { PaneLoader } from '@skyhook-io/k8s-ui'
import { useAPIResources } from '../../api/apiResources'
import { useCapabilities, useRadarFeature } from '../../api/client'
import { useKagentWorkspace, type KagentRef } from '../../api/kagent'
import { useConnection } from '../../context/ConnectionContext'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { useIntegrationSidebars } from '../../integrations/sidebarWorkspaces'
import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'
import { refToSelectedResource } from '../../utils/navigation'
import { WorkspaceFrame } from '../workspace/WorkspaceFrame'
import { RefreshFailedNotice, ScreenBody } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { KagentAgentsTable, KagentMCPServers, KagentModels } from './KagentLists'
import { KagentOverview } from './KagentOverview'
import { CoverageNotice, KagentGate, useKagentRuntime } from './shared'
import { KAGENT_KINDS, kagentPath, parseKagentRoute } from './routes'
import { KagentDetailPage } from './KagentDetailPage'

export function KagentView({ namespaces, onOpenResource }: WorkspaceScreenProps) {
  const route = parseKagentRoute(useLocation().pathname)
  const { connection } = useConnection()
  const feature = useRadarFeature('kagentWorkspace')
  const { isPending: checking } = useCapabilities()
  const query = useKagentWorkspace(namespaces, !route.detail)
  const runtime = useKagentRuntime(
    namespaces,
    !route.detail && (route.screen === 'overview' || route.screen === 'agents') && feature.support === 'supported',
  )
  const { data: apiResources } = useAPIResources()
  const sidebars = useIntegrationSidebars({ apiResources, namespaces })
  const compact = useMediaQuery('(max-width: 1300px)')
  const navigate = useWorkspaceNavigate()
  const onOpen = (ref: KagentRef) => {
    const path = kagentPath(ref, connection.context)
    if (path) navigate(path)
    else onOpenResource(refToSelectedResource(ref))
  }
  const plural =
    route.detail?.plural ??
    (route.screen === 'agents'
      ? 'agents'
      : route.screen === 'models'
        ? 'modelconfigs'
        : route.screen === 'mcpservers'
          ? 'remotemcpservers'
          : undefined)
  if (feature.support === 'unknown' && checking)
    return <PaneLoader label="Checking kagent workspace support…" className="flex-1" />
  return (
    <WorkspaceFrame
      namespaces={namespaces}
      categoryWorkspaces={sidebars}
      compactNavigation={compact}
      selectedKind={plural ? { name: plural, kind: KAGENT_KINDS[plural].kind, group: 'api.kagent.dev' } : null}
    >
      {route.detail ? (
        <KagentDetailPage target={route.detail} onOpenResource={onOpenResource} />
      ) : (
        <KagentGate
          support={feature.support}
          checking={checking}
          loading={feature.support === 'supported' && query.isPending}
          error={query.isError && !query.data ? query.error : undefined}
          installed={query.data?.installed}
        >
          {query.data && (
            <ScreenBody>
              <header className="flex flex-wrap items-baseline justify-between gap-2">
                <h1 className="text-lg font-semibold text-theme-text-primary">
                  <span className="font-normal text-theme-text-tertiary">kagent / </span>
                  {route.screen === 'overview'
                    ? 'Overview'
                    : route.screen === 'mcpservers'
                      ? 'MCP servers'
                      : route.screen === 'models'
                        ? 'Models'
                        : 'Agents'}
                </h1>
                <button
                  type="button"
                  onClick={() => {
                    void query.refetch()
                    if ((route.screen === 'overview' || route.screen === 'agents') && runtime.tier?.state === 'connected')
                      void runtime.query.refetch()
                  }}
                  className="btn-secondary px-2.5 py-1 text-xs"
                >
                  Refresh
                </button>
              </header>
              <RefreshFailedNotice queries={[query, runtime.query]} />
              <CoverageNotice coverage={query.data.coverage} />
              {route.screen === 'overview' ? (
                <KagentOverview namespaces={namespaces} data={query.data} runtime={runtime} onOpen={onOpen} />
              ) : route.screen === 'agents' ? (
                <KagentAgentsTable data={query.data} runtime={runtime} onOpen={onOpen} />
              ) : route.screen === 'mcpservers' ? (
                <KagentMCPServers data={query.data} onOpen={onOpen} />
              ) : (
                <KagentModels data={query.data} onOpen={onOpen} />
              )}
            </ScreenBody>
          )}
        </KagentGate>
      )}
    </WorkspaceFrame>
  )
}
