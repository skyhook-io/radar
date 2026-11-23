import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import type { KagentWorkspace } from '../../api/kagent'
import { ApiError } from '../../api/client'
import { KagentGate, CoverageNotice, SessionValue, type KagentRuntime } from './shared'
import { KagentOverview } from './KagentOverview'
import { KagentAgentsTable, KagentMCPServers, KagentModels } from './KagentLists'
import { KagentAgentOverview, KagentDependencyOverview } from './KagentPages'
import { agent, blockedWorkspace, ref, workspace } from './workspace.fixtures'

const state = vi.hoisted(() => ({
  data: undefined as KagentWorkspace | undefined,
  loading: false,
  resourceError: null as unknown,
  resourceData: undefined as any,
  resourceEnabled: [] as boolean[],
  wide: true,
}))
vi.mock('../../api/kagent', async (original) => ({
  ...(await original<typeof import('../../api/kagent')>()),
  useKagentWorkspace: () => ({
    data: state.data,
    isLoading: state.loading,
    error: null,
    dataUpdatedAt: 1,
  }),
}))
vi.mock('../../api/substrateWorkspace', async (original) => ({
  ...(await original<typeof import('../../api/substrateWorkspace')>()),
  useSubstratePool: () => ({ isLoading: false }),
}))
vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  useResource: (_kind: string, _namespace: string, _name: string, _group: string, options: { enabled: boolean }) => {
    state.resourceEnabled.push(options.enabled)
    return { data: state.resourceData, error: state.resourceError, isLoading: false }
  },
  useRadarFeature: () => ({ support: 'supported' }),
}))
vi.mock('../../context/ConnectionContext', () => ({
  useConnection: () => ({ connection: { context: 'test' } }),
}))
vi.mock('../../hooks/useMediaQuery', () => ({
  useMediaQuery: () => state.wide,
}))
vi.mock('./KagentAgentRenderer', () => ({
  KagentAgentRenderer: () => <p>Individual dependency reads</p>,
}))
vi.mock('./shared', async (original) => ({
  ...(await original<typeof import('./shared')>()),
  useKagentRuntime: () => runtime(),
}))
function runtime(): KagentRuntime {
  return {
    rows: [],
    unavailable: 'Substrate is not connected',
    tier: { state: 'notConfigured' },
    completePage: false,
    lowerBound: true,
    query: { error: null, isRefetchError: false, dataUpdatedAt: 1 },
  } as unknown as KagentRuntime
}
const html = (node: ReactNode) => renderToStaticMarkup(<MemoryRouter>{node}</MemoryRouter>).replace(/<!-- -->/g, '')
const open = vi.fn()
beforeEach(() => {
  state.data = undefined
  state.loading = false
  state.resourceError = null
  state.resourceData = undefined
  state.resourceEnabled = []
  state.wide = true
})

describe('workspace loading, empty and version states', () => {
  it('waits for capabilities and data without flashing the generic list', () => {
    for (const support of ['unknown', 'supported'])
      expect(
        html(
          <KagentGate support={support} loading installed>
            {'table'}
          </KagentGate>,
        ),
      ).toContain('Reading kagent configuration')
  })
  it('keeps old Radar on its existing views', () => {
    const out = html(
      <KagentGate support="unsupported" loading={false}>
        {'new table'}
      </KagentGate>,
    )
    expect(out).toContain('existing Agents list and drawer')
    expect(out).not.toContain('new table')
  })
  it('distinguishes not installed, read failure and a complete empty inventory', () => {
    expect(
      html(
        <KagentGate support="supported" loading={false} installed={false}>
          unused
        </KagentGate>,
      ),
    ).toContain('kagent is not installed')
    expect(
      html(
        <KagentGate support="supported" loading={false} error={new Error('unavailable')}>
          unused
        </KagentGate>,
      ),
    ).toContain('Could not read kagent')
    expect(html(<KagentAgentsTable data={workspace()} runtime={runtime()} onOpen={open} />)).toContain('No Agents in this scope')
  })
})
describe('Overview evidence and folding', () => {
  it('qualifies failed, processing and per-role consumer counts over a partial read', () => {
    const w = blockedWorkspace()
    w.counts.agents.lowerBound = true
    w.coverage.agents.state = 'partial'
    w.counts.agentsByLatestChange = { failed: 2, preparing: 3 }
    w.models[0].usedBy.lowerBound = true
    w.models[0].usedBy.byRole = { model: 1 }
    const overview = html(<KagentOverview data={w} runtime={runtime()} onOpen={open} />)
    expect(overview).toContain('≥2 failed')
    expect(overview).toContain('≥3 processing')
    expect(html(<KagentModels data={w} onOpen={open} />)).toContain('Agent model: ≥1')
  })
  it('does not count stale credential conditions as current findings', () => {
    const w = blockedWorkspace()
    w.findings = []
    w.groups = []
    w.models[0].reading.state = 'stale'
    const out = html(<KagentOverview data={w} runtime={runtime()} onOpen={open} />)
    expect(out).toContain('0</span> with credential problems</a>')
  })
  it('keeps a matched Agent beneath the model and presents impact and next step', () => {
    const out = html(<KagentOverview data={blockedWorkspace()} runtime={runtime()} onOpen={open} />)
    expect(out).toContain('first reported cause')
    expect(out).toContain('blocked-edit')
    expect(out).toContain('Create Secret models/missing')
    expect(out).toContain('2 configuration findings in 1 group')
    expect(out).not.toContain('healthy conversations')
  })
  it('qualifies an empty partial answer and names missing list permission', () => {
    const w = workspace()
    w.counts.findings.lowerBound = true
    w.coverage.models = { state: 'denied', deniedNamespaces: ['models'] }
    const out = html(
      <>
        <CoverageNotice coverage={w.coverage} />
        <KagentOverview data={w} runtime={runtime()} onOpen={open} />
      </>,
    )
    expect(out).toContain('Nothing in what Radar can read needs attention')
    expect(out).toContain('list modelconfigs (api.kagent.dev)')
    expect(out).toContain('in namespace models')
  })
  it('shows all-good evidence and unavailable runtime without fabricating session zeroes', () => {
    const w = workspace()
    w.agents = [agent()]
    w.counts.agents.n = 1
    w.counts.agentsByLatestChange = { prepared: 1 }
    const out = html(<KagentOverview data={w} runtime={runtime()} onOpen={open} />)
    expect(out).toContain('Nothing needs attention')
    expect(out).toContain('1 prepared')
    expect(out).toContain('Substrate is not connected')
    expect(out).not.toContain('0 sessions')
  })
})
describe.each([true, false])('inventory tables, wide=%s', (wide) => {
  it('separates blocked edits from the previous new-sessions revision', () => {
    state.wide = wide
    const out = html(<KagentAgentsTable data={blockedWorkspace()} runtime={runtime()} onOpen={open} />)
    expect(out).toContain('Blocked')
    expect(out).toContain('abcdef012345')
    expect(out).toContain('API key Secret missing')
  })
  it('never turns failed discovery into zero offered tools', () => {
    state.wide = wide
    const w = workspace()
    w.mcpServers = [
      {
        ref: ref('RemoteMCPServer', 'mcp'),
        reading: {
          state: 'failed',
          tone: 'alert',
          text: 'Discovery failed',
          summary: 'Cannot discover tools',
        },
        failing: true,
        toolsOffered: [],
        toolsOfferedKnown: false,
        usedBy: {
          count: 0,
          lowerBound: true,
          consumers: 'noneSeen',
          agents: [],
        },
      },
    ]
    const out = html(<KagentMCPServers data={w} onOpen={open} />)
    expect(out).toContain('Controller discovery failing')
    expect(out).toContain('Unknown')
    expect(out).toContain('No Agent consumers seen')
  })
  it('shows Secret coordinates and missing keys, never credential values', () => {
    state.wide = wide
    const out = html(<KagentModels data={blockedWorkspace()} onOpen={open} />)
    expect(out).toContain('models/missing')
    expect(out).toContain('OPENAI_API_KEY')
    expect(out).toContain('kagent 1.0.1 does not report')
  })
})
describe('object pages', () => {
  it('keeps dependencies outside the namespace pin unread, including after a cached 404', () => {
    state.data = blockedWorkspace()
    const a = state.data.agents[0]
    a.references[0].state = 'unread'
    a.references[0].unread = 'outside_namespace_pin'
    a.references[0].reading = undefined
    state.data.models = []
    state.resourceError = new ApiError('outside namespace scope', 404)
    const out = html(<KagentAgentOverview resource={{ metadata: a.ref }} onOpen={open} />)
    expect(out).toContain('Not read: outside_namespace_pin')
    expect(out).not.toContain('Absent')
    expect(state.resourceEnabled).toEqual([false])
  })
  it('redacts credentials in direct Harness and MCP reads as well as the get-only MCP page', () => {
    state.data = workspace()
    const a = agent()
    a.references = [{ from: a.ref, target: ref('Harness', 'harness'), role: 'harness', field: 'spec.harnessRef', state: 'present' }]
    state.data.agents = [a]
    state.resourceData = { apiVersion: 'api.kagent.dev/v1alpha3', kind: 'Harness', spec: { workload: { image: 'https://user:IMAGE_PASSWORD@registry.invalid/image?token=IMAGE_TOKEN' } } }
    const harness = html(<KagentAgentOverview resource={{ metadata: a.ref }} onOpen={open} />)
    expect(harness).toContain('[REDACTED]')
    expect(harness).not.toContain('IMAGE_PASSWORD')
    expect(harness).not.toContain('IMAGE_TOKEN')
    const server = { apiVersion: 'api.kagent.dev/v1alpha3', kind: 'RemoteMCPServer', metadata: ref('RemoteMCPServer', 'tools'), status: { conditions: [{ type: 'Accepted', status: 'True' }], discoveredTools: [{ name: 'https://user:TOOL_PASSWORD@tools.invalid?token=TOOL_TOKEN' }] } }
    a.references = [{ from: a.ref, target: server.metadata, role: 'mcpServer', field: 'tools', state: 'unread', unread: 'denied' }]
    state.resourceData = server
    const chain = html(<KagentAgentOverview resource={{ metadata: a.ref }} onOpen={open} />)
    state.data = undefined
    const page = html(<KagentDependencyOverview resource={server} onOpen={open} />)
    for (const out of [chain, page]) {
      expect(out).toContain('[REDACTED]')
      expect(out).not.toContain('TOOL_PASSWORD')
      expect(out).not.toContain('TOOL_TOKEN')
    }
    expect(server.status.discoveredTools[0].name).toContain('TOOL_PASSWORD')
  })
  it('shows the Agent loading state before composing a chain', () => {
    state.loading = true
    const out = html(<KagentAgentOverview resource={{ metadata: { namespace: 'agents', name: 'helper' } }} onOpen={open} />)
    expect(out).toContain('Reading Agent configuration')
    expect(out).not.toContain('No revision prepared')
  })
  it.each(['preparing', 'notProcessed'])('qualifies the configuration while the latest change is %s', (latest) => {
    state.data = blockedWorkspace()
    state.data.agents[0].latestChange.state = latest
    state.data.agents[0].reading.state = latest === 'notProcessed' ? 'stale' : 'preparing'
    const out = html(<KagentAgentOverview resource={{ metadata: { namespace: 'agents', name: 'blocked-edit' } }} onOpen={open} />)
    expect(out).toContain(
      latest === 'notProcessed'
        ? 'Configuration of the latest change (not yet processed)'
        : 'Configuration of the latest change (processing)',
    )
    expect(out).toContain('New sessions still run revision abcdef012345')
  })
  it('folds offered tools while keeping asked-for and missing tools visible', () => {
    state.data = workspace()
    const a = agent()
    const target = ref('RemoteMCPServer', 'tools')
    a.references = [
      {
        from: a.ref,
        target,
        role: 'mcpServer',
        field: 'template.inline.tools',
        state: 'present',
        tools: ['missing-tool'],
      },
    ]
    state.data.agents = [a]
    state.data.mcpServers = [
      {
        ref: target,
        reading: {
          state: 'ready',
          tone: 'healthy',
          text: 'Accepted',
          summary: 'Tools discovered',
        },
        failing: false,
        toolsOfferedKnown: true,
        toolsOffered: ['get-pods', 'get-events'],
        usedBy: { count: 1, lowerBound: false, consumers: 'seen', agents: [] },
      },
    ]
    const out = html(
      <KagentAgentOverview
        resource={{
          metadata: { namespace: a.ref.namespace, name: a.ref.name },
        }}
        onOpen={open}
      />,
    )
    expect(out).toContain('Tools asked: missing-tool')
    expect(out).toContain('Not offered: missing-tool')
    expect(out).toContain('2 tools')
    expect(out).toContain('aria-expanded="false"')
  })
  it('explains a blocked latest change without reconstructing the prepared configuration', () => {
    state.data = blockedWorkspace()
    const out = html(<KagentAgentOverview resource={{ metadata: { namespace: 'agents', name: 'blocked-edit' } }} onOpen={open} />)
    expect(out).toContain('Configuration of the latest change (blocked)')
    expect(out).toContain('prepared before this change')
    expect(out).toContain('Conversation outcomes: not visible to Radar')
    expect(out).not.toContain('prepared 2')
  })
  it('reads a list-denied dependency individually and names its missing get grant', () => {
    state.data = blockedWorkspace()
    const a = state.data.agents[0]
    a.references[0].state = 'unread'
    a.references[0].unread = 'denied'
    state.data.models = []
    state.resourceError = new ApiError('forbidden', 403)
    const out = html(<KagentAgentOverview resource={{ metadata: { namespace: 'agents', name: a.ref.name } }} onOpen={open} />)
    expect(out).toContain('get modelconfigs (api.kagent.dev)')
    expect(out).toContain('Unread')
  })
  it('keeps individual reads available when the Agent list is denied', () => {
    state.data = workspace()
    expect(html(<KagentAgentOverview resource={{ metadata: { namespace: 'agents', name: 'helper' } }} onOpen={open} />)).toContain(
      'Individual dependency reads',
    )
  })
  it('shows model reading, credential reference and consumers', () => {
    state.data = blockedWorkspace()
    const out = html(
      <KagentDependencyOverview
        resource={{
          kind: 'ModelConfig',
          metadata: { namespace: 'models', name: 'broken-model' },
        }}
        onOpen={open}
      />,
    )
    expect(out).toContain('Used by')
    expect(out).toContain('Agent model')
    expect(out).toContain('models/missing')
  })
  it('leaves absent runtime rows unknown after all summary pages arrive', () => {
    const r = runtime()
    r.completePage = true
    expect(html(<SessionValue runtime={r} />)).toContain('No summary for this Agent')
  })
})

it('shows a model sharing refusal ahead of its own healthy reading and names a denied list grant', () => {
  const w = blockedWorkspace()
  w.agents[0].model!.state = 'notPermitted'
  w.agents[0].model!.reading = { state: 'ready', tone: 'healthy', text: 'Accepted', summary: 'Resolved' }
  const refused = html(<KagentAgentsTable data={w} runtime={runtime()} onOpen={open} />)
  expect(refused).toContain('Not permitted by allowedNamespaces')
  expect(refused).not.toContain('Resolved')
  w.agents[0].model!.state = 'unread'
  w.agents[0].model!.unread = 'denied'
  expect(html(<KagentAgentsTable data={w} runtime={runtime()} onOpen={open} />)).toContain('list modelconfigs (api.kagent.dev)')
})

it('keeps a declared dependency’s banner on an Agent beyond the capped impact links', () => {
  state.data = blockedWorkspace()
  state.data.findings[1].affects!.agents = [
    {
      agent: ref('Agent', 'another-agent'),
      effect: 'usesIt',
      roles: ['model'],
    },
  ]
  state.data.findings[1].affects!.count = 1000
  state.data.groups[0].folded = []
  const out = html(<KagentAgentOverview resource={{ metadata: { namespace: 'agents', name: 'blocked-edit' } }} onOpen={open} />)
  expect(out).toContain('Create Secret models/missing')
})

it('offers the generic views after a failed capability check instead of loading forever', () => {
  const out = html(
    <KagentGate support="unknown" checking={false} loading={false}>
      unused
    </KagentGate>,
  )
  expect(out).toContain('Could not check kagent workspace support')
  expect(out).toContain('Open Agents')
})

it('uses prose for an unprepared revision and omits placement', () => {
  state.data = workspace()
  const a = agent()
  a.newSessionsRevision = undefined
  a.references = [
    {
      from: a.ref,
      target: { ...ref('WorkerPool', 'pool'), group: 'ate.dev' },
      role: 'workerPool',
      field: 'substrate.workerPoolRef',
      state: 'declared',
    },
  ]
  state.data.agents = [a]
  const out = html(<KagentAgentOverview resource={{ metadata: a.ref }} onOpen={open} />)
  expect(out).toContain('No revision prepared</span>')
  expect(out).not.toContain('font-mono">No revision prepared')
  expect(out).not.toContain('Placement')
  expect(out).toContain('/substrate/workerpools/agents/pool?ctx=test')
  expect(out).toContain('Substrate&#x27;s pools aren&#x27;t readable with this connection')
})

it('keeps one copy of a dependency finding and labels URL removal only when it changed', () => {
  state.data = blockedWorkspace()
  const model = state.data.models[0]
  model.reading.message = 'Unique controller message'
  const out = html(<KagentDependencyOverview resource={{ kind: model.ref.kind, metadata: model.ref }} onOpen={open} />)
  expect(out.match(/kagent&#x27;s message/g)).toHaveLength(1)
  expect(out.match(/Unique controller message/g)).toHaveLength(1)
  state.data = workspace()
  for (const [url, label] of [
    ['https://mcp.example/path', 'URL'],
    ['https://user:pass@mcp.example/path?token=x', 'URL (credentials removed)'],
  ]) {
    const page = html(
      <KagentDependencyOverview
        resource={{
          kind: 'RemoteMCPServer',
          metadata: { namespace: 'agents', name: 'mcp' },
          spec: { url },
        }}
        onOpen={open}
      />,
    )
    expect(page).toContain(label)
    expect(page).not.toContain('URL (redacted)')
    if (label === 'URL') expect(page).not.toContain('credentials removed')
  }
})

it('uses the singular for exactly one session', () => {
  const r = runtime()
  const row = {
    sessions: { total: 1 },
    crashed: 0,
    complete: true,
  } as KagentRuntime['rows'][number]
  expect(html(<SessionValue row={row} runtime={r} />)).toContain('1</span> session')
  expect(html(<SessionValue row={row} runtime={r} />)).not.toContain('sessions')
})

it('links overview problems to filtered lists and filters to the matching findings', () => {
  const w = blockedWorkspace()
  const out = html(<KagentOverview data={w} runtime={runtime()} onOpen={open} />)
  expect(out).toContain('/kagent/mcpservers?status=problems')
  expect(out).toContain('/kagent/models?status=problems')
  w.models.push({ ...w.models[0], ref: ref('ModelConfig', 'healthy-model') })
  const filtered = renderToStaticMarkup(
    <MemoryRouter initialEntries={['/kagent/models?status=problems']}>
      <KagentModels data={w} onOpen={open} />
    </MemoryRouter>,
  )
  expect(filtered).toContain('broken-model')
  expect(filtered).not.toContain('healthy-model')
})
