import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { ResourcesSidebar, type SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'
import type { SubstrateWorkspace } from '../../api/substrateWorkspace'
import type { APIResource } from '../../types'
import { AGENTS_SIDEBAR_CATEGORY, useMergedSidebarWorkspaces } from '../workspace/sidebar'
import { useSubstrateSidebarWorkspace } from '../substrate/useSubstrateSidebarWorkspace'
import { workspace as substrateWorkspace } from '../substrate/substrate.fixtures'
import { useAgentSandboxSidebarWorkspace } from './useAgentSandboxSidebarWorkspace'
import { liveWorkspace } from './workspace.fixtures'

const state = vi.hoisted(() => ({ sandbox: undefined as AgentSandboxWorkspaceResponse | undefined, substrate: undefined as SubstrateWorkspace | undefined }))

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useRadarFeature: () => ({ support: 'supported' }),
}))
vi.mock('../../api/agentSandbox', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/agentSandbox')>()),
  useAgentSandboxWorkspace: () => ({ data: state.sandbox }),
}))
vi.mock('../../api/substrateWorkspace', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/substrateWorkspace')>()),
  useSubstrateWorkspace: () => ({ data: state.substrate }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

const crd = (group: string, kind: string, name: string) => ({ group, version: 'v1alpha1', kind, name, namespaced: true, isCrd: true, verbs: ['list'] }) as APIResource
const SANDBOX_KINDS = [crd('agents.x-k8s.io', 'Sandbox', 'sandboxes'), crd('extensions.agents.x-k8s.io', 'SandboxClaim', 'sandboxclaims'), crd('extensions.agents.x-k8s.io', 'SandboxWarmPool', 'sandboxwarmpools'), crd('extensions.agents.x-k8s.io', 'SandboxTemplate', 'sandboxtemplates')]
const SUBSTRATE_KINDS = [crd('ate.dev', 'WorkerPool', 'workerpools'), crd('ate.dev', 'SandboxConfig', 'sandboxconfigs')]

function render(apiResources: APIResource[], namespaces: string[] = []) {
  let workspaces: Record<string, SidebarCategoryWorkspace> | undefined
  function Host() {
    const substrate = useSubstrateSidebarWorkspace({ apiResources })
    const sandbox = useAgentSandboxSidebarWorkspace({ apiResources, namespaces, active: true })
    workspaces = useMergedSidebarWorkspaces(undefined, undefined, substrate, sandbox)
    return <ResourcesSidebar selectedKind={null} onSelectedKindChange={() => {}} apiResources={apiResources} categoryWorkspaces={workspaces} />
  }
  const html = renderToStaticMarkup(<MemoryRouter><Host /></MemoryRouter>).replace(/<!-- -->/g, '')
  return { html, agents: workspaces?.[AGENTS_SIDEBAR_CATEGORY] }
}

describe('Agent Sandbox in the sidebar', () => {
  it('is its part of the Agents row, the tool beneath, opening to its Overview and three tables', () => {
    state.sandbox = liveWorkspace({ namespaces: ['payments'] })
    const { html, agents } = render(SANDBOX_KINDS, ['payments'])
    expect(agents?.tools).toBe('Agent Sandbox')
    expect(agents?.destinations.map((d) => d.label)).toEqual(['Overview', 'Sandboxes', 'Claims', 'Warm pools'])
    expect(agents?.count).toBe(7)
    // One runtime: the row reads as before, with no section headings.
    expect(html).toMatch(/>Agents<\/span><span[^>]*>Agent Sandbox</)
    expect(html).not.toMatch(/uppercase[^"]*">Sandbox</)
    expect(html).toContain('Counts for namespace payments')
  })
})

describe('The Agents row with both runtimes', () => {
  it('names the job, lists both tools beneath, heads each runtime’s destinations, and adds up their counts', () => {
    state.sandbox = liveWorkspace({ namespaces: [] })
    state.substrate = substrateWorkspace()
    const { html, agents } = render([...SUBSTRATE_KINDS, ...SANDBOX_KINDS])
    expect(html).toMatch(/>Agents<\/span><span[^>]*>Agent Substrate · Agent Sandbox</)
    // One row: the two runtimes no longer list as rows of their own.
    expect(html.match(/aria-label="(Expand|Collapse) Agents"/g)).toHaveLength(1)
    expect(html).not.toContain('Expand Agent Substrate')
    expect(agents?.destinations.map((d) => `${d.section}/${d.label}`)).toEqual([
      'Substrate/Overview', 'Substrate/Actors', 'Substrate/Templates', 'Substrate/Workers', 'Substrate/Pools',
      'Sandbox/Overview', 'Sandbox/Sandboxes', 'Sandbox/Claims', 'Sandbox/Warm pools',
    ])
    expect(new Set(agents?.destinations.map((d) => d.id)).size).toBe(9)
    expect(html.indexOf('>Substrate</div>')).toBeGreaterThan(-1)
    expect(html.indexOf('>Substrate</div>')).toBeLessThan(html.indexOf('>Sandbox</div>'))
    // Substrate's 3 attention items and Sandbox's 7 objects needing attention, with hover breakdown and scope.
    expect(agents).toMatchObject({
      count: 10,
      countLowerBound: false,
      countTitle: 'Attention items — Substrate: 3 · cluster-wide · Sandbox: 7 · all namespaces',
    })
    // The kinds neither runtime has a table for stay under Other kinds, suffixed with their runtime.
    expect(html).toMatch(/Other kinds \(2\)/)
    expect(html).toContain('SandboxConfig')
    expect(html).toContain(' · Substrate')
    expect(html).toContain('SandboxTemplate')
    expect(html).toContain(' · Sandbox')
  })

  it('includes namespace scope in the Agents badge hover breakdown when filtered', () => {
    state.sandbox = liveWorkspace({ namespaces: ['payments'] })
    state.substrate = substrateWorkspace()
    const { agents } = render([...SUBSTRATE_KINDS, ...SANDBOX_KINDS], ['payments'])
    expect(agents?.countTitle).toBe('Attention items — Substrate: 3 · cluster-wide · Sandbox: 7 · namespace payments')
    expect(agents?.scopeNote).toBe('Agent Sandbox: Counts for namespace payments · Agent Substrate: cluster-wide')
  })
})
