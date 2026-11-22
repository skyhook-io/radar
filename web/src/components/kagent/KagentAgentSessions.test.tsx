import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { RadarUpgradeRequirement } from '@skyhook-io/k8s-ui'
import { ApiError } from '../../api/client'
import type { KagentAgentSessions as Sessions } from '../../api/substrateWorkspace'
import { actorRow, connectedTier, counts, minutesAgo } from '../substrate/substrate.fixtures'
import { KagentAgentSessions } from './KagentAgentSessions'

const state = vi.hoisted(() => ({
  data: undefined as Sessions | undefined,
  error: null as unknown,
  isLoading: false,
  kagentUpgrade: null as RadarUpgradeRequirement | null,
}))

vi.mock('../../api/substrateWorkspace', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/substrateWorkspace')>()),
  useKagentAgentSessions: () => ({ data: state.data, error: state.error, isLoading: state.isLoading, upgrade: null, kagentUpgrade: state.kagentUpgrade }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

const agent = { namespace: 'kagent', name: 'k8s-helper' }
const revision = 'aa5e9426eb2ead1c0f65f5c97cf535a9f41038ecd7f8f2b9a7ecc6d925987f40'

function sessions(overrides: Partial<Sessions> = {}): Sessions {
  return {
    apiTier: connectedTier,
    agent: { ...agent, desiredRevision: revision, latestSuccessfulRevision: revision },
    revisions: [{ revision, template: { atespace: 'kagent', name: 'k8s-helper-aa5e9426eb2e' }, newSessions: true, desired: true, sessions: 3 }],
    sessions: counts({ suspended: 2, crashed: 1 }),
    complete: true,
    needAttention: 1,
    attention: [actorRow({ atespace: 'kagent', name: 'session-7c9e6679-7425-40de-944b-e07fc1f90ae7', state: 'crashed', reason: 'exit 1', updateTime: minutesAgo(3),
      kagent: { sessionId: '7c9e6679-7425-40de-944b-e07fc1f90ae7', kind: 'agentSession', agent, newSessions: true, desired: true } })],
    onEarlierRevisions: 0,
    hintedEarlierRevisions: 0,
    sources: ['snapshot'],
    coverage: { actors: { state: 'full' }, kagentAgents: { state: 'full' } },
    ...overrides,
  }
}

const plain = (html: string) => html.replace(/<\/?span[^>]*>/g, '').replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')

function render() {
  return plain(renderToStaticMarkup(<MemoryRouter><KagentAgentSessions agent={agent} /></MemoryRouter>))
}

function reset() {
  state.data = undefined
  state.error = null
  state.isLoading = false
  state.kagentUpgrade = null
}

describe('KagentAgentSessions', () => {
  it('leads with every session, broken down by revision, and lists the ones needing attention', () => {
    reset()
    state.data = sessions()
    const html = render()
    expect(html).toContain('Sessions')
    expect(html).toContain('3 sessions · 3 on the current revision, 0 on earlier ones')
    expect(html).toContain('1 needs attention')
    expect(html).toContain('session 7c9e6679')
    expect(html).toContain('List its sessions')
    // The drawer's Agent section already says where new sessions start and that conversations aren't visible.
    expect(html).not.toContain('New sessions start on')
    expect(html).not.toContain('Conversation outcome')
  })

  it('counts sessions on earlier revisions in the total, not as zero', () => {
    reset()
    state.data = sessions({ sessions: counts({ suspended: 1 }), onEarlierRevisions: 1, needAttention: 0, attention: [],
      revisions: [{ revision, template: { atespace: 'kagent', name: 'k8s-helper-aa5e9426eb2e' }, newSessions: true, desired: true, sessions: 0 }] })
    expect(render()).toContain('1 session · 0 on the current revision, 1 on earlier ones')
  })

  it('reads counts as lower bounds when Substrate’s actors were not all read', () => {
    reset()
    state.data = sessions({ complete: false, coverage: { actors: { state: 'partial', code: 'capExceeded' }, kagentAgents: { state: 'full' } } })
    const html = render()
    expect(html).toContain('at least 3 sessions')
    expect(html).toContain('Lower bounds')
  })

  it('keeps unproven earlier revisions out of the count, and says when another Agent shares a template', () => {
    reset()
    state.data = sessions({
      revisions: [{ revision, template: { atespace: 'kagent', name: 'k8s-helper-aa5e9426eb2e' }, newSessions: true, desired: true, ambiguous: true, sessions: 0 }],
      hintedEarlierRevisions: 2,
    })
    const html = render()
    expect(html).toContain('2 more sessions run on templates named for kagent/k8s-helper that Radar can’t prove are its')
    expect(html).toContain('Radar doesn’t count them')
    expect(html).toContain('Another Agent’s revision makes a template with the same name')
    expect(html).toContain('3 sessions')
  })

  it('shows loading, an older Radar, a refused read and no access as themselves', () => {
    reset()
    state.isLoading = true
    expect(render()).toContain('Reading this Agent’s sessions')

    reset()
    state.kagentUpgrade = { feature: 'kagent Agent sessions in Agent Substrate' }
    expect(render()).toContain('kagent Agent sessions in Agent Substrate')

    reset()
    state.error = new ApiError('no sign-in', 403, { error: 'no sign-in', error_code: 'sharedRadar' })
    expect(render()).toContain('Radar doesn’t read Substrate’s API here')

    reset()
    state.error = new ApiError('no access', 403, { error: 'no access' })
    expect(render()).toContain('You can’t list kagent Agents in namespace kagent')

    reset()
    state.error = new ApiError('not read', 503, { error: 'not read', error_code: 'incomplete' })
    expect(render()).toContain('hasn’t read kagent’s Agents in namespace kagent yet')
  })
})
