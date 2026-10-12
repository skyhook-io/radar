import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import type { IssuesResponse, SubjectIssuesResponse } from '../../api/client'
import { IssuesPane } from './IssuesPane'

const state = vi.hoisted(() => ({
  fleet: { data: undefined as IssuesResponse | undefined, isLoading: false, error: null as Error | null, dataUpdatedAt: 0, refetch: vi.fn() },
  subject: { data: undefined as SubjectIssuesResponse | undefined, isLoading: false, error: null as Error | null, dataUpdatedAt: 0, refetch: vi.fn() },
}))
vi.mock('../../api/client', () => ({ useIssues: () => state.fleet, useSubjectIssues: () => state.subject }))
vi.mock('../../api/apiResources', () => ({ useAPIResources: () => ({ data: undefined }), karpenterCapacityAvailable: () => false }))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useCapabilitiesContext: () => ({}) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { state: 'connected' } }) }))
vi.mock('../diagnose/LocalDiagnoseAction', () => ({ IssueDiagnoseButton: () => null }))

const subjectUrl = '/issues?kind=Pod&resource=db%2Fpg-1'
const issue = {
  id: 'pod-failed', severity: 'critical' as const, source: 'problem', category: 'workload', category_group: 'workload', grouping_scope: 'resource',
  kind: 'Pod', group: '', namespace: 'db', name: 'pg-1', reason: 'CrashLoopBackOff', message: 'The selected Pod keeps crashing',
}

function render(url = subjectUrl, namespaces: string[] = []) {
  return renderToString(<MemoryRouter initialEntries={[url]}><IssuesPane namespaces={namespaces} onNavigateToResource={() => {}} /></MemoryRouter>).replaceAll('<!-- -->', '')
}

beforeEach(() => {
  state.fleet = { data: undefined, isLoading: false, error: null, dataUpdatedAt: 0, refetch: vi.fn() }
  state.subject = { data: { issues: [issue], coverage: 'ok' }, isLoading: false, error: null, dataUpdatedAt: 0, refetch: vi.fn() }
})

describe('Issues page query ownership', () => {
  it.each(['loading', 'failed'] as const)('renders subject results while the fleet query is %s', (fleetState) => {
    state.fleet.isLoading = fleetState === 'loading'
    state.fleet.error = fleetState === 'failed' ? new Error('Fleet unavailable') : null
    const html = render()
    expect(html).toContain('The selected Pod keeps crashing')
    expect(html).toContain('Showing issues about Pod')
    expect(html).not.toContain('Failed to load issues')
    expect(html).not.toContain('Loading issues…')
  })

  it('uses the subject visibility rather than stale fleet visibility', () => {
    state.fleet.data = { issues: [], visibility: { impact: 'Fleet-only limitation' } }
    state.subject.data!.visibility = { state: 'limited', impact: 'Subject evidence is incomplete.' }
    const html = render()
    expect(html).toContain('Subject evidence is incomplete.')
    expect(html).not.toContain('Fleet-only limitation')
  })

  it.each(['loading', 'failed'] as const)('reports a %s subject lookup independently of fleet failure', (subjectState) => {
    state.fleet.error = new Error('Fleet unavailable')
    state.subject.data = undefined
    state.subject.isLoading = subjectState === 'loading'
    state.subject.error = subjectState === 'failed' ? new Error('Subject unavailable') : null
    const html = render()
    expect(html).toContain(subjectState === 'loading' ? 'Checking issues about Pod' : 'Subject unavailable')
    expect(html).not.toContain('Failed to load issues')
    expect(html).not.toContain('none now')
  })

  it('explains a hidden subject even when the fleet query fails', () => {
    state.fleet.error = new Error('Fleet unavailable')
    const html = render(subjectUrl, ['other'])
    expect(html).toContain('namespace filter hides')
    expect(html).not.toContain('The selected Pod keeps crashing')
    expect(html).not.toContain('Failed to load issues')
    expect(html).not.toContain('Auto-updating')
  })

  it.each(['loading', 'failed'] as const)('retains the fleet %s state on the unfiltered page', (fleetState) => {
    state.fleet.isLoading = fleetState === 'loading'
    state.fleet.error = fleetState === 'failed' ? new Error('Fleet unavailable') : null
    expect(render('/issues')).toContain(fleetState === 'loading' ? 'Loading issues…' : 'Failed to load issues')
  })
})
