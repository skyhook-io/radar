import { Badge } from '@skyhook-io/k8s-ui'
import { healthToSeverity } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { readKagent } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kagent'
import type { ResourceHost, KindListProps } from '../../integrations/resourceHost'
import { refToSelectedResource } from '../../utils/navigation'
import { useConnection } from '../../context/ConnectionContext'
import { useCapabilities, useRadarFeature } from '../../api/client'
import { useKagentWorkspace, type KagentRef } from '../../api/kagent'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { DrawerTrailBack } from '../workspace/DrawerTrailBack'
import { RefreshFailedNotice, ScreenBody } from '../workspace/layout'
import { KagentAgentRenderer } from './KagentAgentRenderer'
import { KagentAgentOverview, KagentDependencyOverview } from './KagentPages'
import { KagentAgentsTable, KagentMCPServers, KagentModels } from './KagentLists'
import { CoverageNotice, KagentGate, useKagentRuntime, changeText } from './shared'
import { KAGENT_GROUP, KAGENT_KINDS, kagentPath } from './routes'
import { veleroKindListMode } from '../velero/routes'

function KagentResourceSummary({ resource, onNavigate }: { resource: any; onNavigate?: KindListProps['onInspect'] }) {
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const onOpen = (ref: KagentRef) => {
    const path = kagentPath(ref, connection.context)
    if (path) navigate(path)
    else onNavigate?.(refToSelectedResource(ref))
  }
  return (
    <div className="p-5">
      {resource.kind === 'Agent' ? (
        <KagentAgentOverview resource={resource} onOpen={onOpen} />
      ) : (
        <KagentDependencyOverview resource={resource} onOpen={onOpen} />
      )}
    </div>
  )
}
function KagentKindList({ screen, namespaces, onInspect }: KindListProps & { screen: 'agents' | 'mcpservers' | 'models' }) {
  const feature = useRadarFeature('kagentWorkspace')
  const { isPending: checking } = useCapabilities()
  const query = useKagentWorkspace(namespaces)
  const runtime = useKagentRuntime(namespaces, screen === 'agents')
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const onOpen = (ref: KagentRef) => {
    const path = kagentPath(ref, connection.context)
    if (path) navigate(path)
    else onInspect(refToSelectedResource(ref))
  }
  return (
    <KagentGate
      support={feature.support}
      checking={checking}
      loading={query.isPending}
      error={query.error && !query.data ? query.error : undefined}
      installed={query.data?.installed}
    >
      {query.data && (
        <ScreenBody>
          <RefreshFailedNotice queries={[query]} />
          <CoverageNotice coverage={query.data.coverage} />
          {screen === 'agents' ? (
            <KagentAgentsTable data={query.data} runtime={runtime} onOpen={onOpen} />
          ) : screen === 'mcpservers' ? (
            <KagentMCPServers data={query.data} onOpen={onOpen} />
          ) : (
            <KagentModels data={query.data} onOpen={onOpen} />
          )}
        </ScreenBody>
      )}
    </KagentGate>
  )
}
export const kagentResourceHost: ResourceHost = {
  id: 'kagent',
  feature: 'kagentWorkspace',
  renderers: { KagentAgentRenderer },
  resources: Object.keys(KAGENT_KINDS).map((name) => ({ name, group: KAGENT_GROUP })),
  detailOwnership: ['summary', 'destination'],
  expandedPath: (resource, context, tab) => {
    const plural = Object.keys(KAGENT_KINDS).find(
      (p) => p === resource.kind || KAGENT_KINDS[p as keyof typeof KAGENT_KINDS].kind === resource.kind,
    )
    if (!plural) return null
    const path = kagentPath({ ...resource, kind: KAGENT_KINDS[plural as keyof typeof KAGENT_KINDS].kind, group: KAGENT_GROUP }, context)
    return path && tab ? `${path}${path.includes('?') ? '&' : '?'}tab=${tab}` : path
  },
  detailRedirect: (resource, params) => {
    const plural = Object.keys(KAGENT_KINDS).find(
      (p) => p === resource.kind || KAGENT_KINDS[p as keyof typeof KAGENT_KINDS].kind === resource.kind,
    )
    if (!plural) return null
    const path = kagentPath({ ...resource, kind: KAGENT_KINDS[plural as keyof typeof KAGENT_KINDS].kind, group: KAGENT_GROUP })!
    const search = new URLSearchParams(params)
    search.delete('apiGroup')
    return `${path}${search.size ? `?${search}` : ''}`
  },
  detailSlots: () => ({
    renderStatusBadge: (resource) => {
      const r = readKagent(resource)
      if (!r) return null
      const label =
        resource.kind === 'Agent'
          ? `Latest change ${r.state === 'ready' ? 'prepared' : changeText(r.state === 'stale' ? 'notProcessed' : r.state).toLowerCase()}`
          : resource.kind === 'ModelConfig' && r.state === 'ready'
            ? 'Resolved'
            : resource.kind === 'RemoteMCPServer' && r.state === 'ready'
              ? 'Controller discovery OK'
              : r.text
      return <Badge severity={healthToSeverity(r.tone)}>{label}</Badge>
    },
    renderSummary: (props) => <KagentResourceSummary resource={props.resource} onNavigate={props.onNavigate} />,
  }),
  drawerNavigation: {
    ownsPath: (path) => path === '/kagent' || path.startsWith('/kagent/'),
    Component: ({ resource }) => (
      <DrawerTrailBack
        resource={resource}
        pathPrefix="/kagent"
        kindLabel={(plural) => KAGENT_KINDS[plural as keyof typeof KAGENT_KINDS]?.kind}
      />
    ),
  },
  kindLists: [
    {
      kind: { name: 'agents', kind: 'Agent', group: KAGENT_GROUP },
      title: 'kagent Agents',
      mode: veleroKindListMode,
      Component: (props) => <KagentKindList {...props} screen="agents" />,
    },
    {
      kind: { name: 'remotemcpservers', kind: 'RemoteMCPServer', group: KAGENT_GROUP },
      title: 'kagent MCP servers',
      mode: veleroKindListMode,
      Component: (props) => <KagentKindList {...props} screen="mcpservers" />,
    },
    {
      kind: { name: 'modelconfigs', kind: 'ModelConfig', group: KAGENT_GROUP },
      title: 'kagent models',
      mode: veleroKindListMode,
      Component: (props) => <KagentKindList {...props} screen="models" />,
    },
  ],
}
