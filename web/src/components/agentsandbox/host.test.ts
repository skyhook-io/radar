import { describe, expect, it } from 'vitest'
import { composeResourceHosts } from '../../integrations/resourceHost'
import { resourceHosts } from '../../integrations/resourceHosts'
import { agentSandboxResourceHost } from './host'
import { agentSandboxWorkspaceRoute } from './workspaceRoute'

describe('Agent Sandbox registration', () => {
  it('owns the three kind lists without colliding with another host', () => {
    expect(resourceHosts).toContain(agentSandboxResourceHost)
    expect(() => composeResourceHosts(resourceHosts)).not.toThrow()
    expect(agentSandboxResourceHost.kindLists?.map((l) => `${l.kind.group}/${l.kind.name}`)).toEqual([
      'agents.x-k8s.io/sandboxes',
      'extensions.agents.x-k8s.io/sandboxclaims',
      'extensions.agents.x-k8s.io/sandboxwarmpools',
    ])
    // Read from the generic lists every Radar serves: no capability decides them.
    for (const list of agentSandboxResourceHost.kindLists ?? []) expect(list.mode('unsupported', false)).toBe('view')
  })

  it('applies the namespace filter and owns only its own path', () => {
    expect(agentSandboxWorkspaceRoute.namespaceScope).toBe('view')
    expect(agentSandboxWorkspaceRoute.namespaceFilterTooltip).toBeUndefined()
    expect(agentSandboxWorkspaceRoute.pageTitle('/agent-sandbox')).toBe('Agent Sandbox overview')
    expect(agentSandboxResourceHost.drawerNavigation?.ownsPath('/agent-sandbox')).toBe(true)
    expect(agentSandboxResourceHost.drawerNavigation?.ownsPath('/agent-sandbox-other')).toBe(false)
  })
})
