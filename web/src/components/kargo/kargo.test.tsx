import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import type { Issue } from '@skyhook-io/k8s-ui'
import type { KargoProjectResponse, KargoProjectSummary } from '../../api/kargo'
import { KargoProject } from './KargoProject'
import { kargoIssueProblem, lagTone, pipelineColumns, promotionFailure, stageVerification } from './presentation'
import { chooseKargoProject } from './routes'
import { kargoProblemCount } from './useKargoSidebarWorkspace'
import { project, stage } from './kargo.fixtures'

const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')

function render(data: KargoProjectResponse) {
  const query = { data, error: null, isLoading: false, isFetching: false, refetch: async () => ({}) as never, isRefetchError: false, dataUpdatedAt: Date.now() }
  return text(renderToStaticMarkup(<MemoryRouter><KargoProject query={query} onInspect={() => {}} inspected={null} /></MemoryRouter>))
}

describe('Kargo pipeline', () => {
  it('shows stages left to right with version, verification, last promotion and why the newest version is not there', () => {
    const html = render(project())
    expect(html.indexOf('>test<')).toBeLessThan(html.indexOf('>uat<'))
    expect(html.indexOf('>uat<')).toBeLessThan(html.indexOf('>prod<'))
    expect(html).toContain('illocutionary-skunk')
    expect(html).toContain('nginx:1.31.9')
    expect(html).toContain('since 2h ago')
    expect(html).toContain('Metric "error-rate" assessed Failed')
    expect(html).toContain('authentication required')
    expect(html).toContain('At step git-push')
  })

  it('says a failed upstream check blocks the version, in red with its analysis run, and never suggests approving', () => {
    const html = render(project())
    const prod = html.slice(html.indexOf('>prod<'))
    expect(prod).toContain('Failed verification in uat (error-rate). It can’t reach prod until it passes there.')
    expect(prod).toContain('Open analysis run')
    expect(html).not.toMatch(/approv/i)
    expect(lagTone(project().stages[2].whyNotHere[0])).toBe('unhealthy')
  })

  it('leads a failed promotion with its cause and keeps Kargo’s full message for the tooltip', () => {
    // A live Kargo v1.12.7 Errored promotion; failureCause is the server's Cause.
    const cause = "error pushing commits to remote: error pushing branch: error executing cmd [/usr/bin/git push origin HEAD:stage/test]: fatal: could not read Username for 'http://gitea.gitea.svc.cluster.local:3000': No such device or address"
    const failure = `step "promo-process::step-6" met error threshold of 1: error running step "promo-process::step-6": ${cause}`
    const last = { name: 'test.01', phase: 'Errored', failedStep: 'promo-process::step-6', failure, failureCause: cause }
    const shown = promotionFailure(last)
    expect(shown.cause.startsWith('error pushing commits to remote: … ')).toBe(true)
    expect(shown.cause).toContain("could not read Username for 'http://gitea.gitea.svc.cluster.local:3000': No such device or address")
    expect(shown.cause.length).toBeLessThan(cause.length)
    expect(shown.full).toBe(failure)
    const html = render(project({ stages: [stage({ lastPromotion: last })] }))
    expect(html).toContain(shown.cause)
    expect(html).toContain('At step promo-process::step-6')
    expect(html).not.toContain('met error threshold')
  })

  it('passes a short or unknown failure through and says when Kargo recorded none', () => {
    const longHead = `error executing cmd [git clone https://git.example.com/${'a'.repeat(120)}.git]: exit status 128: fatal: Authentication failed for the repository`
    const longShown = promotionFailure({ failure: longHead, failureCause: longHead }).cause
    expect(longShown.startsWith('… ')).toBe(true)
    expect(longShown).toContain('fatal: Authentication failed for the repository')
    expect(longShown.length).toBeLessThan(longHead.length)
    const twoParts = `error executing cmd [git clone https://git.example.com/${'b'.repeat(160)}.git]: authentication failed`
    expect(promotionFailure({ failure: twoParts, failureCause: twoParts }).cause).toBe('… authentication failed')
    expect(promotionFailure({ failure: 'change freeze until Monday', failureCause: 'change freeze until Monday' }).cause).toBe('change freeze until Monday')
    expect(promotionFailure({ failedStep: 'push' })).toEqual({ cause: 'Kargo recorded no reason.', step: 'push', full: 'Kargo recorded no reason.' })
  })

  it('keeps versions but says aliases and since are unavailable when Freight can’t be read', () => {
    const data = project({ freightRead: false, coverage: { stages: { state: 'full' }, freight: { state: 'denied' }, warehouses: { state: 'denied' } } })
    data.stages = data.stages.map((s) => ({ ...s, current: s.current.map((f) => ({ name: f.name, origin: f.origin, versions: f.versions })) }))
    data.warehouses = []
    const html = render(data)
    expect(html).toContain('You can’t read Freight in this project')
    expect(html).toContain('nginx:1.31.9')
    expect(html).not.toContain('illocutionary-skunk')
    expect(html).toContain('since unknown')
    expect(html).toContain('No access to warehouses.')
    expect(html).not.toContain('This project has no warehouses.')
  })

  it('says why is unknown instead of guessing', () => {
    const html = render(project({ stages: [stage({ whyNotHere: [{ origin: 'kargo-demo', state: 'unknown', reason: 'unknown', cause: 'promotionRunning', message: 'A promotion to test is running.' }] })] }))
    expect(html).toContain('Why: unknown. A promotion to test is running.')
  })

  it('shows Kargo findings from Issues as callouts', () => {
    const issue: Issue = { id: 'i1', severity: 'warning', source: 'condition', category: 'operator_condition_failed', category_group: 'control_plane', grouping_scope: 'unknown', kind: 'Promotion', group: 'kargo.akuity.io', namespace: 'kargo-demo', name: 'prod.01', reason: 'KargoPromotionFailed', message: 'Promotion to stage prod errored at step git-push: authentication required' } as Issue
    const html = render(project({ issues: [issue] }))
    expect(html).toContain('Kargo promotion failed')
    expect(kargoIssueProblem(issue).subject).toEqual({ kind: 'Promotion', group: 'kargo.akuity.io', namespace: 'kargo-demo', name: 'prod.01' })
  })
})

describe('Kargo presentation', () => {
  it('groups stages into columns by depth', () => {
    const cols = pipelineColumns([stage({ name: 'a', depth: 0 }), stage({ name: 'b', depth: 1 }), stage({ name: 'c', depth: 1 })])
    expect(cols.map((c) => c.map((s) => s.name))).toEqual([['a'], ['b', 'c']])
  })

  it('tells not set up from not run yet', () => {
    expect(stageVerification(stage()).text).toBe('Not set up')
    // Kargo records arrival as verified when the Stage has no checks.
    expect(stageVerification(stage({ verification: { phase: 'Successful' } })).text).toBe('Not set up')
    expect(stageVerification(stage({ verificationConfigured: true })).tone).toBe('unknown')
  })

  it('reads behind as neutral unless the last promotion failed', () => {
    expect(lagTone({ origin: 'w', state: 'behind', reason: 'autoPromotionOff', message: '' })).toBe('neutral')
    expect(lagTone({ origin: 'w', state: 'behind', reason: 'lastPromotionFailed', message: '' })).toBe('unhealthy')
    expect(lagTone({ origin: 'w', state: 'unknown', reason: 'unknown', message: '' })).toBe('unknown')
  })

  it('picks the requested project, else one deploying to the view, else a view project, else one with problems, else one with stages', () => {
    const p = (name: string, extra: Partial<KargoProjectSummary> = {}): KargoProjectSummary => ({ name, stages: 1, problems: 0, ...extra })
    const empty = p('empty-demo', { stages: 0 })
    const demo = p('kargo-demo', { deployNamespaces: ['kargo-demo-prod', 'kargo-demo-uat'] })
    expect(chooseKargoProject([empty, demo], 'empty-demo', ['kargo-demo-prod'])).toEqual({ project: 'empty-demo' })
    expect(chooseKargoProject([empty, demo], null, ['kargo-demo-prod'])).toEqual({ project: 'kargo-demo' })
    expect(chooseKargoProject([empty, demo, p('b')], null, ['b'])).toEqual({ project: 'b' })
    expect(chooseKargoProject([empty, p('a'), p('b', { problems: 2 })], null, ['x'])).toEqual({ project: 'b' })
    expect(chooseKargoProject([empty, p('a')], null, [])).toEqual({ project: 'a' })
    expect(chooseKargoProject([empty], null, [])).toEqual({ project: 'empty-demo' })
    expect(chooseKargoProject([empty, demo], 'waiting-demo', ['kargo-demo-prod'])).toEqual({ project: 'kargo-demo', missing: 'waiting-demo' })
    expect(chooseKargoProject([], 'waiting-demo', [])).toEqual({ project: undefined, missing: 'waiting-demo' })
  })

  it('shows a notice above the project when the URL named one that isn’t listed', () => {
    const query = { data: project(), error: null, isLoading: false, isFetching: false, refetch: async () => ({}) as never, isRefetchError: false, dataUpdatedAt: Date.now() }
    const html = text(renderToStaticMarkup(<MemoryRouter><KargoProject query={query} onInspect={() => {}} inspected={null} notice={<span>Can’t find project waiting-demo</span>} /></MemoryRouter>))
    expect(html.indexOf('Can’t find project waiting-demo')).toBeLessThan(html.indexOf('>test<'))
  })

  it('counts problems once and as a lower bound when some Stages were not read', () => {
    expect(kargoProblemCount({ installed: true, projects: [{ name: 'a', stages: 2, problems: 1 }, { name: 'b', stages: 1, problems: 2 }], coverage: { stages: { state: 'full' } } }).count).toBe(3)
    const partial = kargoProblemCount({ installed: true, projects: [], coverage: { stages: { state: 'partial' } } })
    expect(partial.count).toBe(0)
    expect(partial.lowerBound).toBe(true)
  })
})
