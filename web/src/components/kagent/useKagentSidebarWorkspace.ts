import { useMemo } from 'react'
import { useLocation } from 'react-router-dom'
import { Bot } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useKagentWorkspace } from '../../api/kagent'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { AGENTS_SIDEBAR_CATEGORY } from '../workspace/sidebar'
import { KAGENT_AGENTS_PATH, KAGENT_AGENT_KIND, KAGENT_GENERIC_AGENTS_PATH, KAGENT_KINDS } from './routes'

export const KAGENT_SIDEBAR_SECTION = 'kagent'
export function kagentAgentsServed(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === KAGENT_AGENT_KIND.group && r.name === KAGENT_AGENT_KIND.name)
}
export function useKagentSidebarWorkspace({
  apiResources,
  namespaces = [],
}: {
  apiResources: APIResource[] | undefined
  namespaces?: string[]
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const location = useLocation()
  const served = kagentAgentsServed(apiResources)
  const feature = useRadarFeature('kagentWorkspace')
  const sessions = useRadarFeature('kagentSessionSummary')
  const workspace = useKagentWorkspace(namespaces, served)
  const ready = feature.support === 'supported'
  return useMemo(() => {
    if (!served) return undefined
    const count = workspace.data?.counts.findings
    const destination = (id: string, label: string, path: string, kind?: typeof KAGENT_AGENT_KIND) => ({
      id: `kagent-${id}`,
      label,
      section: KAGENT_SIDEBAR_SECTION,
      active:
        id === 'sessions'
          ? location.pathname === '/substrate/actors' && new URLSearchParams(location.search).get('kagent') === 'sessions'
          : location.pathname === path,
      kind,
      onSelect: () => navigate(path),
    })
    return {
      [AGENTS_SIDEBAR_CATEGORY]: {
        tools: 'kagent',
        icon: Bot,
        count: ready ? (count ? (count.n === 0 && count.lowerBound ? null : count.n) : null) : undefined,
        countLowerBound: ready ? (count?.lowerBound ?? true) : undefined,
        countTitle: ready ? 'Configuration findings; runtime crashes and pool capacity are counted by Agent Substrate.' : undefined,
        destinations: ready
          ? [
              destination('overview', 'Overview', '/kagent'),
              destination('agents', 'Agents', KAGENT_AGENTS_PATH, KAGENT_AGENT_KIND),
              destination('mcpservers', 'MCP servers', '/kagent/mcpservers', {
                name: 'remotemcpservers',
                kind: KAGENT_KINDS.remotemcpservers.kind,
                group: KAGENT_AGENT_KIND.group,
              }),
              destination('models', 'Models', '/kagent/models', {
                name: 'modelconfigs',
                kind: KAGENT_KINDS.modelconfigs.kind,
                group: KAGENT_AGENT_KIND.group,
              }),
              ...(sessions.support === 'supported'
                ? [
                    destination(
                      'sessions',
                      'Sessions →',
                      `/substrate/actors?kagent=sessions${namespaces.length ? `&namespaces=${encodeURIComponent(namespaces.join(','))}` : ''}`,
                    ),
                  ]
                : []),
            ]
          : [destination('agents', 'Agents', KAGENT_GENERIC_AGENTS_PATH, KAGENT_AGENT_KIND)],
      },
    }
  }, [served, ready, workspace.data, location.pathname, location.search, navigate, sessions.support, namespaces.join(',')])
}
