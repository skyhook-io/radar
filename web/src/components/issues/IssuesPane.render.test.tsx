// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { createMemoryRouter, MemoryRouter, RouterProvider } from 'react-router-dom'
import type { IssuesResponse, SubjectIssuesResponse } from '../../api/client'
import { IssuesPane } from './IssuesPane'

const state = vi.hoisted(() => ({
  applications: vi.fn(() => ({ data: undefined })),
  fleet: { data: undefined as IssuesResponse | undefined, isLoading: false, error: null as Error | null, dataUpdatedAt: 0, refetch: vi.fn() },
  subject: { data: undefined as SubjectIssuesResponse | undefined, isLoading: false, error: null as Error | null, dataUpdatedAt: 0, refetch: vi.fn() },
}))
vi.mock('../../api/client', () => ({ useApplications: state.applications, useIssues: () => state.fleet, useSubjectIssues: () => state.subject, useIssueCorrelation: () => ({ data: undefined, error: null, isFetching: false, dataUpdatedAt: 0, refetch: vi.fn() }), useIssueRowChanges: () => undefined }))
vi.mock('../../api/apiResources', () => ({ useAPIResources: () => ({ data: undefined }), karpenterCapacityAvailable: () => false }))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useCapabilitiesContext: () => ({}) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { state: 'connected' } }) }))
vi.mock('../diagnose/LocalDiagnoseAction', () => ({ IssueDiagnoseButton: () => null }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const subjectUrl = '/issues?kind=Pod&resource=db%2Fpg-1'
const issue = {
  id: 'pod-failed', severity: 'critical' as const, source: 'problem', category: 'workload', category_group: 'workload', grouping_scope: 'resource',
  kind: 'Pod', group: '', namespace: 'db', name: 'pg-1', reason: 'CrashLoopBackOff', message: 'The selected Pod keeps crashing',
}

function render(url = subjectUrl, namespaces: string[] = []) {
  return renderToString(<MemoryRouter initialEntries={[url]}><IssuesPane namespaces={namespaces} onNavigateToResource={() => {}} /></MemoryRouter>).replaceAll('<!-- -->', '')
}

beforeEach(() => {
  state.applications.mockClear()
  state.fleet = { data: undefined, isLoading: false, error: null, dataUpdatedAt: 0, refetch: vi.fn() }
  state.subject = { data: { issues: [issue], coverage: 'ok' }, isLoading: false, error: null, dataUpdatedAt: 0, refetch: vi.fn() }
})

describe('Issues page query ownership', () => {
  it('only requests scoped application inventory for lists that can group', () => {
    state.fleet.data = { issues: [issue] }
    render('/issues', ['db'])
    expect(state.applications).toHaveBeenLastCalledWith(['db'], { enabled: false })
    state.fleet.data.issues.push({ ...issue, id: 'other' })
    render('/issues', ['db'])
    expect(state.applications).toHaveBeenLastCalledWith(['db'], { enabled: true })
    render(subjectUrl, ['db'])
    expect(state.applications).toHaveBeenLastCalledWith(['db'], { enabled: false })
  })

  it('replaces a link when triaging and reveals the next linked issue through an active filter', async () => {
    state.fleet.data = { issues: [issue, { ...issue, id: 'replicas', severity: 'warning', reason: 'Unavailable' }] }
    const router = createMemoryRouter([
      { path: '/', element: <div>Home</div> },
      { path: '/issues', element: <IssuesPane namespaces={['db']} onNavigateToResource={() => {}} /> },
    ], { initialEntries: ['/', '/issues?namespaces=db&issue=pod-failed'], initialIndex: 1 })
    const container = document.createElement('div')
    const root = createRoot(container)
    const row = (id: string) => container.querySelector(`[data-app-issue-row=":${id}"]`)
    const toggle = (id: string) => row(id)!.querySelector<HTMLElement>('[role=button][aria-expanded]')!
    try {
      await act(async () => { root.render(<RouterProvider router={router} />) })
      expect(toggle('pod-failed').getAttribute('aria-expanded')).toBe('true')
      await act(async () => { toggle('replicas').click() })
      expect(router.state.location.search).toBe('?namespaces=db')
      expect(toggle('replicas').getAttribute('aria-expanded')).toBe('true')
      await act(async () => { await router.navigate(-1) })
      expect(router.state.location.pathname).toBe('/')

      await act(async () => { await router.navigate('/issues?namespaces=db&issue=replicas') })
      const critical = [...container.querySelectorAll('button')].find(button => button.textContent === '1Critical')!
      await act(async () => { critical.click() })
      expect(row('replicas')).toBeNull()
      expect(router.state.location.search).toBe('?namespaces=db')
      await act(async () => { await router.navigate('/issues?namespaces=db&issue=replicas') })
      expect(toggle('replicas').getAttribute('aria-expanded')).toBe('true')
      expect(row('pod-failed')).toBeTruthy()
    } finally {
      await act(async () => { root.unmount() })
      router.dispose()
    }
  })

  it('explains a missing issue link while keeping the current issues visible', () => {
    state.fleet.data = { issues: [issue] }
    const html = render('/issues?issue=absent')
    expect(html).toContain('The linked issue is outside the current results.')
    expect(html).toContain(issue.message)
    expect(render('/issues?issue=pod-failed')).not.toContain('outside the current results')
  })

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

describe('Issues page when Radar could not read Helm releases', () => {
  const line = "Helm releases couldn't be checked, so failed or stuck releases may be missing."
  const text = (html: string) => html.replace(/&#x27;|&#39;/g, "'")

  it('shows no all-clear when nothing else was found', () => {
    state.fleet.data = { issues: [], total: 0, total_matched: 0, unchecked_sources: [{ source: 'helm', reason: 'read_failed' }] }
    const html = text(render('/issues'))
    expect(html).toContain('No issues found')
    expect(html).toContain(line)
    expect(html).not.toContain('No issues detected')
    expect(html.split(line).length - 1).toBe(1)
  })

  it('says so above the rows when there are other issues', () => {
    state.fleet.data = { issues: [issue], total: 1, total_matched: 1, unchecked_sources: [{ source: 'helm', reason: 'read_failed' }] }
    const html = text(render('/issues'))
    expect(html).toContain(line)
    expect(html).toContain('The selected Pod keeps crashing')
  })

  it.each([true, false])('keeps the last unchecked result and retry after a failed refresh (rows: %s)', (hasRows) => {
    state.fleet.data = { issues: hasRows ? [issue] : [], unchecked_sources: [{ source: 'helm', reason: 'read_failed' }] }
    state.fleet.error = new Error('Refresh unavailable')
    const html = text(render('/issues'))
    expect(html).toContain('Issue refresh failed. Showing the last successful result.')
    expect(html).toContain(line)
    expect(html).toContain('Check again')
    expect(html).not.toContain('Failed to load issues')
    expect(html).not.toContain('No issues detected')
    if (hasRows) expect(html).toContain('The selected Pod keeps crashing')
  })

  it('does not show an all-clear from a cached empty result after refresh fails', () => {
    state.fleet.data = { issues: [], total: 0 }
    state.fleet.error = new Error('Refresh unavailable')
    const html = text(render('/issues'))
    expect(html).toContain('No issues in the last successful result')
    expect(html).not.toContain('No issues detected')
    expect(html).not.toContain('Failed to load issues')
  })

  it('shows no all-clear for a source this version does not know, with a generic line', () => {
    state.fleet.data = { issues: [], total: 0, total_matched: 0, unchecked_sources: [{ source: 'some-new-source', reason: 'read_failed' }] }
    const html = text(render('/issues'))
    expect(html).toContain('No issues found')
    expect(html).toContain("Some issue checks couldn't run, so issues may be missing.")
    expect(html).not.toContain('some-new-source')
    expect(html).not.toContain('No issues detected')
  })

  it('keeps the all-clear for a Radar that read Helm or predates the field', () => {
    state.fleet.data = { issues: [], total: 0, total_matched: 0 }
    const html = text(render('/issues'))
    expect(html).toContain('No issues detected')
    expect(html).not.toContain('couldn')
  })
})

describe('Helm notice in subject mode', () => {
  it('is left out: the per-resource lookup never reads Helm storage', () => {
    state.fleet.data = { issues: [], unchecked_sources: [{ source: 'helm', reason: 'read_failed' }] }
    const html = render()
    expect(html).toContain('The selected Pod keeps crashing')
    expect(html).not.toContain('Helm releases couldn')
  })
})
