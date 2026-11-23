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
  wide: true,
}))
vi.mock('../../api/kagent', async (original) => ({
  ...(await original<typeof import('../../api/kagent')>()),
  useKagentWorkspace: () => ({ data: state.data, isLoading: state.loading, error: null, dataUpdatedAt: 1 }),
}))
vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  useResource: () => ({ error: state.resourceError, isLoading: false }),
  useRadarFeature: () => ({ support: 'supported' }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
vi.mock('../../hooks/useMediaQuery', () => ({ useMediaQuery: () => state.wide }))
vi.mock('./KagentAgentRenderer', () => ({ KagentAgentRenderer: () => <p>Individual dependency reads</p> }))
vi.mock('./shared', async (original) => ({ ...(await original<typeof import('./shared')>()), useKagentRuntime: () => runtime() }))
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
  it('does not count stale credential conditions as current findings', () => {
    const w = blockedWorkspace()
    w.findings = []
    w.groups = []
    w.models[0].reading.state = 'stale'
    const out = html(<KagentOverview data={w} runtime={runtime()} onOpen={open} />)
    expect(out).toContain('0</span> with credential problems')
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
        reading: { state: 'failed', tone: 'alert', text: 'Discovery failed', summary: 'Cannot discover tools' },
        failing: true,
        toolsOffered: [],
        toolsOfferedKnown: false,
        usedBy: { count: 0, lowerBound: true, consumers: 'noneSeen', agents: [] },
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
    a.references = [{ from: a.ref, target, role: 'mcpServer', field: 'template.inline.tools', state: 'present', tools: ['missing-tool'] }]
    state.data.agents = [a]
    state.data.mcpServers = [
      {
        ref: target,
        reading: { state: 'ready', tone: 'healthy', text: 'Accepted', summary: 'Tools discovered' },
        failing: false,
        toolsOfferedKnown: true,
        toolsOffered: ['get-pods', 'get-events'],
        usedBy: { count: 1, lowerBound: false, consumers: 'seen', agents: [] },
      },
    ]
    const out = html(<KagentAgentOverview resource={{ metadata: { namespace: a.ref.namespace, name: a.ref.name } }} onOpen={open} />)
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
        resource={{ kind: 'ModelConfig', metadata: { namespace: 'models', name: 'broken-model' } }}
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

it('keeps a declared dependency’s banner on an Agent beyond the capped impact links', () => {
  state.data = blockedWorkspace()
  state.data.findings[1].affects!.agents = [{ agent: ref('Agent', 'another-agent'), effect: 'usesIt', roles: ['model'] }]
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
