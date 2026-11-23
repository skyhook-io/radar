import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, expect, it, vi } from 'vitest'
import type { KagentAgentSessions } from '../../api/substrateWorkspace'
import { ApiError } from '../../api/client'
import { connectedTier, counts } from '../substrate/substrate.fixtures'
import { KagentRevisions, KagentSessionsTab } from './KagentRuntimeTabs'
const state = vi.hoisted(() => ({
  support: 'supported',
  loading: false,
  error: null as unknown,
  data: undefined as KagentAgentSessions | undefined,
  enabled: false,
}))
vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  useRadarFeature: () => ({ support: state.support }),
}))
vi.mock('../../api/substrateWorkspace', async (original) => ({
  ...(await original<typeof import('../../api/substrateWorkspace')>()),
  useKagentAgentSessions: () => ({ data: state.data, isLoading: state.loading, error: state.error }),
  useSubstrateActors: (_query: unknown, enabled: boolean) => {
    state.enabled = enabled
    return { error: state.error, isLoading: state.loading }
  },
  useSubstrateTemplates: () => ({ data: { templates: [], coverage: { actors: { state: 'full' } } } }),
  useSubstrateTemplate: () => ({ data: undefined }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
const agent = { namespace: 'agents', name: 'helper' }
const render = (node: React.ReactNode) => renderToStaticMarkup(<MemoryRouter>{node}</MemoryRouter>).replace(/<!-- -->/g, '')
beforeEach(() => {
  state.support = 'supported'
  state.loading = false
  state.error = null
  state.data = undefined
})
it('shows unavailable tiers and never asks an old Radar for filtered actors', () => {
  state.support = 'unsupported'
  expect(render(<KagentSessionsTab agent={agent} />)).toContain('Sessions need a newer Radar')
  expect(state.enabled).toBe(false)
  expect(render(<KagentRevisions agent={agent} />)).toContain('Revisions from Substrate need a newer Radar')
})
it('renders revision loading and an unavailable Substrate tier explicitly', () => {
  state.loading = true
  expect(render(<KagentRevisions agent={agent} />)).toContain('Reading revisions')
  state.loading = false
  state.error = new ApiError('not configured', 409, { error_code: 'notConfigured' })
  expect(render(<KagentRevisions agent={agent} />)).toContain('can’t count')
  expect(render(<KagentSessionsTab agent={agent} />)).not.toContain('0 sessions')
})
it('labels desired and prepared revisions separately and keeps unprovable sessions as hints', () => {
  state.data = {
    apiTier: connectedTier,
    agent: { ...agent, desiredRevision: '1111111111112222', latestSuccessfulRevision: 'abcdef0123456789' },
    revisions: [
      {
        revision: 'abcdef0123456789',
        template: { atespace: 'agents', name: 'helper-abcdef012345' },
        newSessions: true,
        ambiguous: true,
        sessions: 0,
      },
    ],
    sessions: counts({ suspended: 1 }),
    onEarlierRevisions: 1,
    complete: false,
    needAttention: 0,
    attention: [],
    hintedEarlierRevisions: 3,
    sources: ['snapshot'],
    coverage: { actors: { state: 'partial' }, templates: { state: 'partial' }, kagentAgents: { state: 'full' } },
  }
  const out = render(<KagentRevisions agent={agent} />)
  expect(out).toContain('111111111111')
  expect(out).toContain('abcdef012345')
  expect(out).toContain('ambiguous attribution')
  expect(out).toContain('unprovable earlier revisions')
  expect(out).not.toContain('prepared 1h')
})
