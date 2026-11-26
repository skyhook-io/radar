// @vitest-environment jsdom

import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { SubstrateStatus, SubstrateWorkspace } from '../../api/substrateWorkspace'
import { SUBSTRATE_NO_PROBLEMS, SubstrateOverview } from './SubstrateOverview'
import { connectedTier, connection, counts, notConnectedWorkspace, apiDisabledTier, mintDeniedTier, ownerTier, poolShortRow, sharedTier, status, workspace } from './substrate.fixtures'

const state = vi.hoisted(() => ({ connection: undefined as unknown }))

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  usePrometheusStatus: () => ({ data: { connected: true } }),
}))
vi.mock('../../api/substrateWorkspace', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/substrateWorkspace')>()),
  useSubstrateConnection: () => ({ data: state.connection, isError: false }),
  useUpdateSubstrateConnection: () => ({ mutate: vi.fn(), isPending: false }),
}))

// renderToStaticMarkup escapes apostrophes; compare against the text a reader sees.
const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')
// Names wrap only after a hyphen, so each segment is its own span; read them as the reader does.
const plain = (html: string) => html.replace(/<\/?span[^>]*>/g, '')
// The words alone: glossary terms and tooltips sit inside sentences.
const words = (html: string) => html.replace(/<[^>]+>/g, '')

function render(data: SubstrateWorkspace, st: SubstrateStatus | undefined = status({ apiTier: data.apiTier })) {
  return text(renderToStaticMarkup(
    <MemoryRouter>
      <SubstrateOverview data={data} status={st} onOpenSubject={() => {}} />
    </MemoryRouter>,
  ))
}

describe('Agent Substrate overview, connected', () => {
  it('says where its values come from: the API, who Radar reads as, Prometheus', () => {
    const html = render(workspace(), status({ apiTier: connectedTier }))
    expect(html).toContain('ate-system/api:443')
    expect(html).toContain('system:serviceaccount:ate-system:ate-client')
    expect(html).toMatch(/Prometheus<\/span><span[^>]*>connected/)
    expect(html).toContain('Change connection')
  })

  it('reads the sourced At a glance rows from Substrate’s counts', () => {
    const html = render(workspace())
    expect(html).toContain('3 pools · 1 short of workers · 1 full')
    expect(html).toContain('18 active · 1 draining · 1 unavailable')
    expect(html).toContain('212 of 236 actor slots used (24 free, 21 of them on batch-pool)')
    const doc = new DOMParser().parseFromString(html, 'text/html')
    const poolsLink = [...doc.querySelectorAll('button')].find((b) => b.textContent === 'Pools →')
    expect(poolsLink?.parentElement?.textContent).toContain('18 active')
    expect(poolsLink?.parentElement?.textContent).not.toContain('actor slots used')
    expect(poolsLink?.parentElement?.nextElementSibling?.textContent).toContain('actor slots used')
    expect(poolsLink?.parentElement?.nextElementSibling?.lastElementChild?.textContent).toBe('Kubernetes WorkerPools and Substrate API')
    expect(html).toContain('1,284 total, 14 crashed')
    expect(words(html)).toContain('6 templates · 1 golden snapshot failed')
    // One run of text, not an element per part: the facts row is a flex row
    // whose gap would set each comma apart from the word before it.
    expect(html).toContain('6 templates · 1 ')
  })

  it('leads with Needs attention in the server’s order and labels broader crash links', () => {
    const html = render(workspace())
    expect(html).not.toContain('aria-label="Actors by state"')
    expect(html.indexOf('Needs attention')).toBeLessThan(html.indexOf('At a glance'))
    const attention = html.slice(html.indexOf('Needs attention'))
    // The fixture's order is the server's ranking: crash, pool, golden snapshot. No category regrouping.
    const crash = attention.indexOf('14 actors are crashed')
    const pool = attention.indexOf('is short of workers')
    const golden = attention.indexOf('has no golden snapshot')
    expect(crash).toBeLessThan(pool)
    expect(pool).toBeLessThan(golden)
    // Each row names its kind on the subject line, as Velero's and Agent Sandbox's rows do.
    expect(attention).toMatch(/<span>Pool <span[^>]*><span><button[^>]*><span[^>]*>agent-pool</)
    expect(attention).toContain('14 actors are crashed')
    // Where most of them are, as a concentration with a link, not a cause.
    expect(words(attention)).toContain('11 of the 14 use template research / langgraph-research')
    expect(attention).toContain('View all crashed actors →')
    expect(attention).toContain('research / langgraph-research')
    expect(attention).toContain('Substrate’s messages')
    expect(attention).toContain('By template')
    expect(attention).toContain('Next step: ')
  })

  it('keeps the healthy state compact and the check evidence in a closed disclosure', () => {
    const html = render(workspace({ needsAttention: [] }))
    expect(html).toContain(SUBSTRATE_NO_PROBLEMS)
    const checks = html.slice(html.indexOf('What was checked'), html.indexOf('At a glance'))
    expect(checks).toContain('inert=""')
    expect(checks).toContain('Crashed actors')
    expect(checks).toContain('Not assessed by any check')
    expect(checks).toContain('Request routing and egress')
    expect(html).toContain('>Pools →</button>')
    expect(html).not.toContain('Slots on active workers</th>')
    expect(plain(html)).toContain('support-bots')
  })

  it('says checks waiting on Prometheus’s first answer are still running, and ones Prometheus can’t serve could not run', () => {
    const collecting = 'Radar is collecting Substrate’s metrics from Prometheus; they show on the next refresh'
    const base = workspace()
    const ws = {
      ...base,
      checks: [
        ...base.checks,
        { rule: 'poolService' as const, label: 'Substrate’s pool capacity verdicts (Prometheus)', ran: false, why: collecting, collecting: true },
        { rule: 'nodeUploadsOverdue' as const, label: 'Overdue snapshot uploads per node (Prometheus)', ran: false, why: collecting, collecting: true },
      ],
      service: { ...base.service, why: collecting, collecting: true },
    }
    let html = words(render(ws))
    expect(html).toContain('What was checked3 of 5 checks ran · 2 still running')
    expect(html).not.toContain('checks ran · 2 could not')
    expect(html).toContain(`Still running: ${collecting}`)
    // The Delivery fact doesn't claim an absence Radar hasn't read yet.
    expect(html).toContain('being collected from Prometheus')
    expect(html).not.toContain('no Substrate metrics')
    // Prometheus not connected: the honest state is that they could not run, and why.
    const down = { ...ws, checks: ws.checks.map((c) => (c.collecting ? { ...c, why: 'Prometheus is not connected', collecting: false } : c)), service: base.service }
    html = words(render(down))
    expect(html).toContain('What was checked3 of 5 checks ran · 2 could not')
    expect(html).not.toContain('still running')
    expect(html).toContain('Substrate’s pool capacity verdicts (Prometheus): Prometheus is not connected')
  })

  it('defines atespace where the Overview first names it', () => {
    const raw = render(workspace())
    expect(raw).toMatch(/decoration-dotted[^>]*>Atespaces<\/span>/)
  })

  it('says an atespace Substrate denied is unread, never empty', () => {
    const html = render(workspace())
    const row = html.slice(html.indexOf('restricted'))
    expect(row).toContain('No access')
  })

  it('gives an atespace read in part a lower bound, and one not read no number', () => {
    const html = render(workspace({
      atespaces: [
        { name: 'budgetcut', actorsRead: 'partial', actors: counts({ running: 3 }), templatesRead: 'full', templates: 1 },
        { name: 'outofscope', actorsRead: 'unread', templatesRead: 'unread' },
      ],
    }))
    const partial = html.slice(html.indexOf('budgetcut'), html.indexOf('outofscope'))
    expect(partial).toContain('aria-label="At least: Radar read only part of this atespace’s actors"')
    const unread = html.slice(html.indexOf('outofscope'))
    expect(unread).toContain('not read')
    expect(unread).not.toMatch(/>0</)
  })

  it('raises no coverage notice for kinds not read because the API tier is down', () => {
    expect(render(notConnectedWorkspace())).not.toContain('Radar couldn’t read all of what this screen shows')
  })

  it('makes counts over a partial read lower bounds, and says what it could not read', () => {
    const base = workspace()
    const html = render(workspace({ needsAttention: [], coverage: { ...base.coverage!, actors: { state: 'partial', deniedAtespaces: ['restricted'] } } }))
    expect(html).toContain('Radar couldn’t read all of what this screen shows')
    expect(html).toContain('Substrate denied atespace restricted')
    expect(html).toContain('aria-label="At least: some of it could not be read"')
    expect(html).toContain(SUBSTRATE_NO_PROBLEMS)
  })
})

describe('Agent Substrate overview, not connected', () => {
  it('keeps what Kubernetes shows, and says actor values are not connected rather than 0', () => {
    state.connection = connection()
    const html = render(notConnectedWorkspace())
    const body = words(html)
    expect(body).toContain('Worker capacity1 pool · 6 Pods ready of 6 desiredPools →Not connectedKubernetes WorkerPools and Substrate API')
    expect(body).toContain('ActorsNot connectedActors →')
    expect(body).toContain('Template startupNot connectedTemplates →')
    expect(body).not.toContain('Actors0')
    expect(html).toContain('Not checked on this read')
    expect(html).toContain('Crashed actors: actors were not read')
    expect(body).toContain('Delivery metricsUnavailable · no Substrate series')
  })

  it('offers Settings → Agent Substrate where actors would be, on a local Radar', () => {
    const html = render(notConnectedWorkspace())
    expect(html).toContain('Connect to Substrate’s API')
    expect(html).toContain('>Open Substrate settings</button>')
    // The form lives in Settings; the workspace never edits the connection.
    expect(html).not.toContain('Who Radar reads Substrate as')
    expect(html).not.toContain('Disconnect')
  })

  it('explains why a shared Radar without sign-in does not read the API, and what turns it on', () => {
    const data = notConnectedWorkspace(sharedTier)
    const html = render(data, status({ apiTier: sharedTier, canConfigure: false }))
    expect(html).toContain('Needs sign-in')
    expect(html).toContain('Turn on sign-in for this Radar')
    expect(html).toContain('>Connection settings</button>')
    expect(html).not.toContain('Open Substrate settings')
  })

  it('reads each failure plainly with a next step', () => {
    const tier = { state: 'failed' as const, code: 'unreachable' as const, reason: 'dial tcp 127.0.0.1:443: connection refused' }
    const html = render(notConnectedWorkspace(tier))
    expect(html).toContain('Substrate API: api server unreachable')
    expect(html).toContain('connection refused')
    expect(html).toContain('Next step: </span>Check that the API server’s Pods are running')
    const denied = render(notConnectedWorkspace({ state: 'failed', code: 'denied', reason: 'permission denied' }))
    expect(denied).toContain('viewer role')
  })
})

describe('Agent Substrate on an in-cluster Radar with sign-in, or Radar Hub', () => {
  it('tells a signed-in person which identity Radar reads as, with nothing to connect or change', () => {
    const html = render(workspace({ apiTier: ownerTier }), status({ apiTier: ownerTier, canConfigure: false }))
    expect(html).toContain('api.ate-system.svc:443</span> as Substrate’s shared client identity (')
    expect(html).toContain('ate-system/ate-client</span>), minted with your Kubernetes access')
    expect(html).toContain('aria-label="About this identity"')
    expect(html).not.toContain('Change connection')
    expect(html).toContain('>Connection settings</button>')
    // Their own access could mint the token: the actors are there.
    expect(words(html)).toContain('Actors1,284 total, 14 crashed')
  })

  it('says when the operator turned the API off, and keeps Kubernetes and Prometheus', () => {
    const data = notConnectedWorkspace(apiDisabledTier)
    const html = render(data, status({ apiTier: apiDisabledTier, canConfigure: false }))
    expect(html).toContain('Turned off for this Radar')
    expect(html).toContain('substrate.api.enabled')
    expect(html).not.toContain('Open Substrate settings')
    expect(html).not.toContain('Substrate API: ')
    expect(html).toContain('Worker capacity')
    expect(html).toContain('Prometheus')
  })

  it('tells a person whose access can’t mint the token what to ask for, and how to check', () => {
    const html = render(notConnectedWorkspace(mintDeniedTier), status({ apiTier: mintDeniedTier, canConfigure: false }))
    expect(html).toContain('Substrate API: couldn’t create the shared identity’s token')
    expect(html).toContain('serviceaccounts/token for ate-client in ate-system')
    expect(html).toContain('kubectl auth can-i create serviceaccounts/ate-client --subresource=token -n ate-system')
    // The banner states the next step once; the notice below only leads to Settings.
    expect(html.split('ask a cluster admin for create on serviceaccounts/token').length - 1).toBe(1)
    expect(html).toContain('Actors, templates and workers aren’t read')
  })

  it('gives a person whose install lacks the shared identity a kubectl check, not a form', () => {
    const tier = { state: 'failed' as const, code: 'notInstalled' as const, identity: { mode: 'sharedAsCaller' as const }, reason: 'ServiceAccount ate-system/ate-client does not exist.' }
    const html = render(notConnectedWorkspace(tier), status({ apiTier: tier, canConfigure: false }))
    expect(html).toContain('Substrate’s shared client identity (ate-system/ate-client) doesn’t exist here.')
    expect(html).toContain('kubectl -n ate-system get sa ate-client'.split(' ').pop()!)
    expect(html).not.toContain('Advanced')
    expect(html).not.toContain('Who Radar reads Substrate as')
  })

  it('sends a local Radar to its settings, and tells a shared one without sign-in why not', () => {
    const local = render(notConnectedWorkspace())
    expect(local).toContain('Open Substrate settings')
    const shared = render(notConnectedWorkspace(sharedTier), status({ apiTier: sharedTier, canConfigure: false }))
    expect(shared).toContain('Needs sign-in')
    expect(shared).not.toContain('org owner')
  })
})

describe('Agent Substrate status line beside the inventory', () => {
  it('never reads a missing Prometheus count as calm, and lets crashed actors and unstored snapshots set the colour', async () => {
    const { serviceFacts } = await import('./SubstrateOverview')
    const upload = { id: 'uploadOverdue', rule: 'uploadOverdue' as const, severity: 'critical' as const, title: 't', reading: 'r', nextStep: 'n', subject: { kind: 'Actors' as const }, count: 24 }
    // Prometheus was read but counted no crashes and no overdue node, while the API lists 15 crashed and 24 unstored.
    const facts = serviceFacts({
      service: { read: true, slowestResume: { pool: { namespace: 'n', name: 'agent-pool' }, value: 4.8 }, poolsUnknown: 2, nodesOverdue: 0, nodesUnknown: 0 },
      summary: { pools: { total: 1, short: 0, full: 0 }, actors: { total: 100, crashed: 15, attentionCount: 24 } },
      needsAttention: [upload],
    })
    const crashes = facts.find((f) => String(f.label).startsWith('Crashes'))!
    expect(crashes.tone).toBe('unhealthy')
    expect(crashes.lowerBound).toBeFalsy()
    expect(renderToStaticMarkup(<>{crashes.value}</>)).toContain('unknown')
    const uploads = facts.find((f) => f.label === 'Uploads')!
    expect(uploads.value).toBe('24 snapshots not stored yet')
    expect(uploads.tone).toBe('unhealthy')
    expect(uploads.lowerBound).toBeFalsy()
    // Nothing healthy-green that the inventory contradicts, and no "≥" before an unknown or zero.
    expect(facts.filter((f) => f.tone === 'healthy').map((f) => f.label)).toEqual(['Resumes (1h)'])
  })

  it('reads a condition in its row’s tone: an unavailable worker, a template no pool matches, an overdue upload are alerts', async () => {
    const { overviewFacts, serviceFacts } = await import('./SubstrateOverview')
    const base = workspace()
    const unavailable = { ...poolShortRow, id: 'workerUnavailable:w', rule: 'workerUnavailable' as const, severity: 'alert' as const, subject: { kind: 'Worker' as const, namespace: 'ns', name: 'w' } }
    const tones = (data: ReturnType<typeof workspace>) => Object.fromEntries(overviewFacts(data).map((f) => [renderToStaticMarkup(<>{f.label}</>).replace(/<[^>]+>/g, ''), f.tone]))
    // Workers: 1 unavailable in the fixture is at risk, orange, with or without its row yet.
    expect(tones(base).Workers).toBe('alert')
    expect(tones({ ...base, needsAttention: [...base.needsAttention, unavailable] }).Workers).toBe('alert')
    // A lost sandbox on a worker is critical: the fact takes the worst row's tone.
    const lost = { ...unavailable, id: 'workerRestarting:w', rule: 'workerRestarting' as const, severity: 'critical' as const }
    expect(tones({ ...base, needsAttention: [...base.needsAttention, lost] }).Workers).toBe('unhealthy')
    // An info row (a drain within its deadline) does not colour it.
    const draining = { ...unavailable, id: 'workerDraining:w', rule: 'workerDraining' as const, severity: 'info' as const }
    const calm = { ...base, summary: { ...base.summary, workers: { ...base.summary.workers!, unavailable: 0 } }, needsAttention: [draining] }
    expect(tones(calm).Workers).toBe('healthy')
    // Templates: one no pool matches is an alert; a failed golden snapshot alone stays a warning.
    const t = base.summary.templates!
    expect(tones({ ...base, summary: { ...base.summary, templates: { ...t, noMatchingPool: 1 } } }).Templates).toBe('alert')
    expect(tones({ ...base, summary: { ...base.summary, templates: { ...t, noMatchingPool: 0, goldenFailed: 1 } }, needsAttention: [] }).Templates).toBe('degraded')
    // Uploads: an alert row while the nodes are up.
    const upload = { ...poolShortRow, id: 'uploadOverdue', rule: 'uploadOverdue' as const, severity: 'alert' as const, count: 3 }
    expect(serviceFacts({ service: base.service, summary: base.summary, needsAttention: [upload] }).find((f) => f.label === 'Uploads')!.tone).toBe('alert')
  })

  it('shows unknown tone for empty partial counts and neutral for complete zero counts', async () => {
    const { overviewFacts } = await import('./SubstrateOverview')
    const data = workspace({ needsAttention: [] })
    data.summary.actors = { ...data.summary.actors!, total: 0, crashed: 0 }
    data.summary.templates = { ...data.summary.templates!, total: 0, goldenFailed: 0, noMatchingPool: 0 }
    const countFacts = () => overviewFacts(data).filter((f) => f.label === 'Actors' || f.label === 'Templates')
    expect(countFacts().map((f) => f.tone)).toEqual(['neutral', 'neutral'])
    data.coverage.actors = { state: 'partial' }
    data.coverage.templates = { state: 'partial' }
    expect(countFacts()).toMatchObject([
      { value: 'Actor count unknown', tone: 'unknown' },
      { value: 'Template count unknown', tone: 'unknown' },
    ])
  })

  it('never reads free slots as free capacity when no worker has the CPU or memory for an actor', async () => {
    const { overviewFacts } = await import('./SubstrateOverview')
    const slotLine = (data: ReturnType<typeof workspace>) => {
      const f = overviewFacts(data).find((f) => words(renderToStaticMarkup(<>{f.label}</>)) === 'Actor slots')!
      return { value: text(String(f.value)), tone: f.tone }
    }
    const base = workspace()
    const pool = { namespace: 'kagent', name: 'agent-pool' }
    // The live case: one worker, 1,000 slots, 1 used, and not the CPU for a second actor.
    const live = { ...base, summary: { ...base.summary, pools: { total: 1, short: 0, full: 0, noRoom: 1 }, slots: { used: 1, total: 1000 }, freeSlots: [{ pool, free: 999, lacking: 'cpu' as const }] } }
    expect(slotLine(live)).toEqual({ value: '1 of 1,000 actor slots used (999 free, but agent-pool’s workers have no CPU left for an actor)', tone: 'degraded' })
    const poolsFact = overviewFacts(live).find((f) => words(renderToStaticMarkup(<>{f.label}</>)) === 'Worker pools')!
    expect(poolsFact.value).toBe('1 pool · 1 with no room for an actor')
    expect(poolsFact.tone).not.toBe('healthy')
    // Mixed: the free count is the room that is left; the rest is named, not added in.
    const mixed = { ...base, summary: { ...base.summary, freeSlots: [{ pool: { namespace: 'batch', name: 'batch-pool' }, free: 21, lacking: 'memory' as const }, { pool: { namespace: 'agent-ns', name: 'agent-pool' }, free: 3 }] } }
    expect(slotLine(mixed)).toEqual({ value: '212 of 236 actor slots used (3 free on agent-pool; 21 more where batch-pool’s workers have no memory left for an actor)', tone: 'healthy' })
    // An older Radar sends no lacking: the line reads as it always did.
    expect(slotLine(base)).toEqual({ value: '212 of 236 actor slots used (24 free, 21 of them on batch-pool)', tone: 'healthy' })
  })

  it('folds an unmeasured Prometheus into one unknown fact', async () => {
    const { serviceFacts } = await import('./SubstrateOverview')
    const facts = serviceFacts({ service: { read: true, poolsUnknown: 3, nodesOverdue: 0, nodesUnknown: 2 }, summary: { pools: { total: 3, short: 0, full: 0 } }, needsAttention: [] })
    expect(facts).toHaveLength(1)
    expect(facts[0]).toMatchObject({ label: 'Delivery (1h)', tone: 'unknown' })
  })
})

it('keeps coverage and refresh warnings visible with the coverage disclosure closed', () => {
  const ws = workspace()
  ws.coverage.actors = { state: 'partial', deniedAtespaces: ['restricted'] }
  ws.apiTier = { ...ws.apiTier, lastError: { code: 'unreachable', at: new Date().toISOString(), message: 'timed out' } }
  ws.atespaces = [{ name: 'partial', actorsRead: 'partial', actors: counts({ running: 2 }), templatesRead: 'partial', templates: 0 }]
  const html = render(ws)
  const doc = new DOMParser().parseFromString(html, 'text/html')
  const notice = [...doc.querySelectorAll('p, div')].find((n) => n.textContent?.startsWith('Radar couldn’t read all of what this screen shows') && n.children.length === 0)
  expect(notice?.textContent).toContain('Substrate denied atespace restricted')
  expect(notice?.closest('[inert]')).toBeNull()
  const failure = [...doc.querySelectorAll('[role="status"]')].find((n) => n.textContent?.startsWith('Last Substrate API read failed'))
  expect(failure?.textContent).toContain('Showing the previous read')
  expect(failure?.textContent).toContain('timed out')
  expect(failure?.closest('.border-theme-border')).not.toBeNull()
  expect(failure?.closest('[inert]')).toBeNull()
  const values = [...doc.querySelectorAll('tbody tr')].find((r) => r.firstElementChild?.textContent === 'partial')?.querySelectorAll('td')
  expect(values?.[4].textContent).toBe('Unknown')
})

it('labels the broader suspended-actors link and makes the crash concentration actionable', () => {
  const ws = workspace()
  ws.needsAttention = [...ws.needsAttention, { ...poolShortRow, id: 'upload', rule: 'uploadOverdue', count: 1, subject: { kind: 'Actors', filter: { state: 'suspended' } } }]
  const doc = new DOMParser().parseFromString(render(ws), 'text/html')
  const buttons = [...doc.querySelectorAll('button')]
  expect(buttons.some((b) => b.textContent === 'View all suspended actors →')).toBe(true)
  const concentration = [...doc.querySelectorAll('div')].find((n) => n.textContent === '11 of the 14 use template research / langgraph-research' && n.firstElementChild?.tagName === 'BUTTON')
  expect(concentration?.querySelector('button')?.textContent).toBe('research / langgraph-research')
})
