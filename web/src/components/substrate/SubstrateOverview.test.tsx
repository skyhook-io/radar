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
      <SubstrateOverview data={data} status={st} onInspect={() => {}} inspected={null} onOpenSubject={() => {}} />
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

  it('reads the status line from Substrate’s counts', () => {
    const html = render(workspace())
    expect(html).toContain('3, 1 short of workers, 1 full')
    expect(html).toContain('18 active, 1 draining, 1 unavailable')
    expect(html).toContain('212 of 236 used (24 free, 21 of them on batch-pool)')
    expect(html).toContain('1,284, 14 crashed')
    expect(words(html)).toContain('6, 1 golden snapshot failed')
    // One run of text, not an element per part: the facts row is a flex row
    // whose gap would set each comma apart from the word before it.
    expect(html).toContain('6, 1 ')
  })

  it('draws the state bar with accessible counts, and lists Needs attention in the server’s order, each row labelled', () => {
    const html = render(workspace())
    expect(html).toMatch(/role="img" aria-label="1,284 actors: Crashed 14, Resuming 6, Suspending 9, Reverting 2, Running 212, Suspended 1,041"/)
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
    expect(plain(attention)).toMatch(/11 of the 14 use template <button[^>]*>research \/ langgraph-research<\/button>/)
    expect(attention).toContain('Show the 14 actors →')
    expect(attention).toContain('research / langgraph-research')
    expect(attention).toContain('Substrate’s messages')
    expect(attention).toContain('By template')
    expect(attention).toContain('Next step: ')
  })

  it('shows the evidence of a healthy installation, not only the absence of problems', () => {
    const html = render(workspace({ needsAttention: [] }))
    // An empty list names what was checked and what no check reads, not "healthy".
    expect(html).toContain(SUBSTRATE_NO_PROBLEMS)
    expect(html).toContain('Crashed actors')
    expect(html).toContain('Not assessed by any check')
    expect(html).toContain('Request routing and egress')
    expect(html).toContain('Worker pools')
    expect(plain(html)).toContain('agent-pool')
    expect(html).toContain('37 of 40 used')
    // Slots count active workers only; the rest of a pool's actors are said to be elsewhere, not left to contradict.
    const ws = workspace()
    ws.pools[0] = { ...ws.pools[0], actors: 40 }
    // Which workers they are on, and what that means, in plain words.
    expect(render(ws)).toContain('+3 actors on unavailable workers, which take no new actors')
    expect(html).toContain('Free actor slots')
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
    expect(html).toContain('What was checked: 3 of 5 checks ran, 2 still running')
    expect(html).not.toContain('could not')
    expect(html).toContain(`Still running: ${collecting}`)
    // The Delivery fact doesn't claim an absence Radar hasn't read yet.
    expect(html).toContain('being collected from Prometheus')
    expect(html).not.toContain('no Substrate metrics')
    // Prometheus not connected: the honest state is that they could not run, and why.
    const down = { ...ws, checks: ws.checks.map((c) => (c.collecting ? { ...c, why: 'Prometheus is not connected', collecting: false } : c)), service: base.service }
    html = words(render(down))
    expect(html).toContain('What was checked: 3 of 5 checks ran, 2 could not')
    expect(html).not.toContain('still running')
    expect(html).toContain('Substrate’s pool capacity verdicts (Prometheus): Prometheus is not connected')
  })

  it('defines atespace where the Overview first names it', () => {
    const raw = render(workspace())
    expect(raw).toMatch(/counted by Substrate across ?(<!-- -->)?\s*(<!-- -->)?\d+(<!-- -->)? (<!-- -->)?<span[^>]*><span[^>]*decoration-dotted[^>]*>atespaces?<\/span>/)
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
    expect(html).toMatch(/Worker Pods:<\/dt><dd[^>]*>(<span[^>]*>)*6 ready of 6/)
    expect(html).toMatch(/Actors<\/span><\/span>:<\/dt><dd[^>]*>(?:<[^>]+>)*Not connected/)
    expect(html).toMatch(/Templates<\/span><\/span>:<\/dt><dd[^>]*>(?:<[^>]+>)*Not connected/)
    expect(html).not.toMatch(/Actors<\/span><\/span>:<\/dt><dd[^>]*>0/)
    expect(plain(html)).toContain('agent-pool')
    expect(html).toContain('Not checked on this read')
    expect(html).toContain('Crashed actors: actors were not read')
    // Delivery from Prometheus is unknown, with why, never zero.
    expect(html).toMatch(/Delivery \(1h\):<\/dt><dd[^>]*>(?:<[^>]+>)*unknown \(no Substrate metrics\)/)
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
    expect(html).toContain('Actor states')
  })

  it('says when the operator turned the API off, and keeps Kubernetes and Prometheus', () => {
    const data = notConnectedWorkspace(apiDisabledTier)
    const html = render(data, status({ apiTier: apiDisabledTier, canConfigure: false }))
    expect(html).toContain('Turned off for this Radar')
    expect(html).toContain('substrate.api.enabled')
    expect(html).not.toContain('Open Substrate settings')
    expect(html).not.toContain('Substrate API: ')
    expect(html).toContain('Worker pools')
    expect(html).toContain('Prometheus')
  })

  it('tells a person whose access can’t mint the token what to ask for, and how to check', () => {
    const html = render(notConnectedWorkspace(mintDeniedTier), status({ apiTier: mintDeniedTier, canConfigure: false }))
    expect(html).toContain('Substrate API: your access can’t create the shared identity’s token')
    expect(html).toContain('serviceaccounts/token for ate-client in ate-system')
    expect(html).toContain('kubectl auth can-i create serviceaccounts/ate-client --subresource=token -n ate-system')
    // The banner states the next step once; the notice below only leads to Settings.
    expect(html.split('Ask a cluster admin for create on serviceaccounts/token').length - 1).toBe(1)
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
      summary: { pools: { total: 1, short: 0, full: 0 }, actors: { total: 100, crashed: 15 } },
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

  it('folds an unmeasured Prometheus into one unknown fact', async () => {
    const { serviceFacts } = await import('./SubstrateOverview')
    const facts = serviceFacts({ service: { read: true, poolsUnknown: 3, nodesOverdue: 0, nodesUnknown: 2 }, summary: { pools: { total: 3, short: 0, full: 0 } }, needsAttention: [] })
    expect(facts).toHaveLength(1)
    expect(facts[0]).toMatchObject({ label: 'Delivery (1h)', tone: 'unknown' })
  })
})
