import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { ResourcesSidebar, type SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'
import type { APIResource } from '../../types'
import { AGENTS_SIDEBAR_CATEGORY, useMergedSidebarWorkspaces } from '../workspace/sidebar'
import { useAgentSandboxSidebarWorkspace } from '../agentsandbox/useAgentSandboxSidebarWorkspace'
import { liveWorkspace } from '../agentsandbox/workspace.fixtures'
import { useKagentSidebarWorkspace } from './useKagentSidebarWorkspace'

const state = vi.hoisted(() => ({ sandbox: undefined as AgentSandboxWorkspaceResponse | undefined }))

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useRadarFeature: () => ({ support: 'supported' }),
}))
vi.mock('../../api/agentSandbox', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/agentSandbox')>()),
  useAgentSandboxWorkspace: () => ({ data: state.sandbox }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

const crd = (group: string, kind: string, name: string) => ({ group, version: 'v1alpha3', kind, name, namespaced: true, isCrd: true, verbs: ['list'] }) as APIResource
const KAGENT_KINDS = [crd('api.kagent.dev', 'Agent', 'agents'), crd('api.kagent.dev', 'ModelConfig', 'modelconfigs'), crd('api.kagent.dev', 'Harness', 'harnesses')]
const SANDBOX_KINDS = [crd('agents.x-k8s.io', 'Sandbox', 'sandboxes'), crd('extensions.agents.x-k8s.io', 'SandboxClaim', 'sandboxclaims'), crd('extensions.agents.x-k8s.io', 'SandboxWarmPool', 'sandboxwarmpools'), crd('extensions.agents.x-k8s.io', 'SandboxTemplate', 'sandboxtemplates')]

function render(apiResources: APIResource[]) {
  let workspaces: Record<string, SidebarCategoryWorkspace> | undefined
  function Host() {
    const sandbox = useAgentSandboxSidebarWorkspace({ apiResources, namespaces: [] })
    const kagent = useKagentSidebarWorkspace({ apiResources })
    workspaces = useMergedSidebarWorkspaces(undefined, undefined, undefined, sandbox, kagent)
    return <ResourcesSidebar selectedKind={null} onSelectedKindChange={() => {}} apiResources={apiResources} categoryWorkspaces={workspaces} />
  }
  const html = renderToStaticMarkup(<MemoryRouter><Host /></MemoryRouter>).replace(/<!-- -->/g, '')
  return { html, agents: workspaces?.[AGENTS_SIDEBAR_CATEGORY] }
}

describe('kagent in the sidebar', () => {
  it('adds an Agents destination for kagent’s Agent list to the Agents row', () => {
    const { html, agents } = render(KAGENT_KINDS)
    expect(agents?.tools).toBe('kagent')
    expect(agents?.destinations).toMatchObject([{ id: 'kagent-agents', label: 'Agents', section: 'kagent', kind: { name: 'agents', group: 'api.kagent.dev' } }])
    expect(agents?.count).toBeUndefined()
    // The Agent kind is reached through the destination, so only kagent's other kinds stay under Other kinds.
    expect(html).toMatch(/Other kinds \(2\)/)
  })

  it('shares the row with Agent Sandbox under a heading of its own', () => {
    state.sandbox = liveWorkspace()
    const { html, agents } = render([...SANDBOX_KINDS, ...KAGENT_KINDS])
    expect(html).toMatch(/>Agents<\/span><span[^>]*>Agent Sandbox · kagent</)
    expect(agents?.destinations.map((d) => `${d.section}/${d.label}`)).toEqual(['Sandbox/Overview', 'Sandbox/Sandboxes', 'Sandbox/Claims', 'Sandbox/Warm pools', 'kagent/Agents'])
    // A part with no count adds nothing: the row keeps Agent Sandbox's.
    expect(agents?.count).toBe(5)
  })

  it('is absent when the cluster doesn’t serve kagent’s Agents', () => {
    expect(render([crd('kagent.dev', 'Agent', 'agents')]).agents).toBeUndefined()
  })
})
