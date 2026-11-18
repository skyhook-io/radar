import type { ComponentProps, ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

import { RadarFeatureUnsupportedError } from '../../api/radarFeatures'
import type { AgentSandboxAttention, AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'
import { AgentSandboxOverview, NOTHING_IN_WHAT_WAS_READ, NOTHING_NEEDS_ATTENTION, overviewFacts } from './AgentSandboxOverview'
import { AgentSandboxWorkspaceGate, attentionAge, attentionDetail, attentionTitle, sidebarCount } from './workspace'
import { emptyWorkspace, liveAttention, liveWorkspace, minutesAgo } from './workspace.fixtures'

// renderToStaticMarkup escapes apostrophes; compare against the text a reader sees.
// Source messages wrap at spaces only, a span per word (wrapAtSpaces); read them as running text.
const text = (html: string) =>
  html
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, '&')
    .replace(/<!-- -->/g, '')
    .replace(/<span class="inline-block max-w-full align-top">([^<]*)<\/span>/g, '$1')
const page = (node: ReactNode) => text(renderToStaticMarkup(<MemoryRouter>{node}</MemoryRouter>))
const overview = (data: AgentSandboxWorkspaceResponse) => page(<AgentSandboxOverview data={data} onClearNamespaces={() => {}} onInspect={() => {}} inspected={null} />)
const fact = (data: AgentSandboxWorkspaceResponse, label: string) => {
  const f = overviewFacts(data).find((x) => x.label === label)
  return typeof f?.value === 'string' ? f.value : undefined
}
const row = (html: string, title: string) => {
  const start = html.indexOf(title)
  const end = html.indexOf('</li>', start)
  return html.slice(start, end)
}

type GateQuery = ComponentProps<typeof AgentSandboxWorkspaceGate>['query']
const idle: GateQuery = { data: undefined, error: null, isLoading: false, isFetching: false, refetch: vi.fn(), isRefetchError: false, dataUpdatedAt: 0 }
const gate = (query: Partial<GateQuery>, opts = { discoveryPending: false, discovered: true }) =>
  page(
    <AgentSandboxWorkspaceGate query={{ ...idle, ...query }} discoveryPending={opts.discoveryPending} discovered={opts.discovered}>
      {() => <div>screen</div>}
    </AgentSandboxWorkspaceGate>,
  )

describe('Agent Sandbox overview gate', () => {
  it('waits, then shows the screen; says when Agent Sandbox is not installed', () => {
    expect(gate({ isLoading: true })).toContain('Loading Agent Sandbox')
    expect(gate({}, { discoveryPending: false, discovered: false })).toContain('Agent Sandbox is not installed')
    expect(gate({ data: liveWorkspace({ installed: false }) })).toContain('Agent Sandbox is not installed')
    expect(gate({ data: liveWorkspace() })).toContain('screen')
  })

  it('tells an older Radar that it needs a newer one, not an error, and keeps the lists usable', () => {
    const html = gate({ error: new RadarFeatureUnsupportedError('agentSandboxWorkspace', { currentVersion: 'v1.16.0' }) })
    expect(html).toContain('needs a newer Radar')
    expect(html).toContain('lists work on this Radar')
    expect(html).not.toContain('Agent Sandbox data unavailable')
  })

  it('offers a retry for a failed read', () => {
    const html = gate({ error: new Error('boom') })
    expect(html).toContain('Agent Sandbox data unavailable')
    expect(html).toContain('Retry')
  })
})

describe('Agent Sandbox overview, from the server summary', () => {
  it('answers "is it working?" in one line from the server counts', () => {
    const data = liveWorkspace()
    expect(fact(data, 'Controller')).toBe('running')
    expect(fact(data, 'Sandboxes')).toBe('9: 0 running, 2 can’t schedule, 1 suspended, 1 suspension blocked, 2 expired, 1 finished, 2 failed')
    expect(fact(data, 'Claims')).toBe('2: 0 bound, 1 waiting, 1 expired')
    expect(fact(data, 'Warm pools')).toBe('0 of 2 sandboxes ready in 1 pool')
  })

  it('marks counts over a partial read with ≥, and a kind the summary left out as unknown, never 0', () => {
    const live = liveWorkspace()
    const partial = liveWorkspace({ counts: { ...live.counts, sandboxes: { ...live.counts.sandboxes!, lowerBound: true } } })
    expect(fact(partial, 'Sandboxes')).toBe('≥9: ≥0 running, ≥2 can’t schedule, ≥1 suspended, ≥1 suspension blocked, ≥2 expired, ≥1 finished, ≥2 failed')
    const leftOut = liveWorkspace({ counts: { ...live.counts, claims: undefined }, coverage: { ...live.coverage, claims: { state: 'denied' } } })
    expect(fact(leftOut, 'Claims')).toBe('unknown — no access')
    const notInstalled = liveWorkspace({ counts: { ...live.counts, warmPools: undefined }, coverage: { ...live.coverage, warmPools: { state: 'notInstalled' } } })
    expect(fact(notInstalled, 'Warm pools')).toBe('not installed')
    const html = overview(leftOut)
    expect(html).toContain('SandboxClaims (no access)')
  })

  it('reads the controller where it looked, and never calls an unread one stopped', () => {
    const c = liveWorkspace().controller
    expect(fact(liveWorkspace({ controller: { ...c, state: 'notFound' } }), 'Controller')).toBe('unknown — not found as agent-sandbox-system/agent-sandbox-controller')
    expect(fact(liveWorkspace({ controller: { ...c, state: 'unread', code: 'user_forbidden' } }), 'Controller')).toBe('unknown — no access')
    // Loading, overdue and not collected are different reasons; read counts are absent unless the Deployment was read.
    const unread = (code: string) => fact(liveWorkspace({ controller: { state: 'unread', namespace: c.namespace, name: c.name, code } }), 'Controller')
    expect(unread('kind_sync_pending')).toBe('unknown — still loading')
    expect(unread('kind_sync_timed_out')).toBe('unknown — loading is overdue')
    expect(unread('radar_kind_not_collected')).toBe('unknown — not collected by Radar')
    expect(unread('outside_namespace_pin')).toBe('unknown — outside Radar’s namespace scope')
    expect(fact(liveWorkspace({ controller: { ...c, state: 'scaledToZero', ready: 0, desired: 0 } }), 'Controller')).toBe('scaled to zero')
    expect(fact(liveWorkspace({ controller: { ...c, state: 'notReady', ready: 0, since: minutesAgo(20) } }), 'Controller')).toBe('not ready for 20m')
  })

  it('lists every row the server sent in the server’s order, each with its next step and the source text folded', () => {
    const html = overview(liveWorkspace())
    const attention = html.slice(html.indexOf('Needs attention'), html.indexOf('>Warm pools</h2>'))
    expect([...attention.matchAll(/data-rule="(\w+)"/g)].map((m) => m[1])).toEqual(liveAttention().map((a) => a.rule))
    // The server's order holds even when it doesn't match the kinds' order.
    const reversed = liveAttention().reverse()
    const html2 = overview(liveWorkspace({ attention: reversed }))
    expect([...html2.matchAll(/data-rule="(\w+)"/g)].map((m) => m[1])).toEqual(reversed.map((a) => a.rule))
    expect(attention.match(/Next step: /g)).toHaveLength(5)
    expect(attention).toContain('Controller’s message')
    expect(attention).toContain('Scheduler’s message')
    expect(attention).toContain('data-rule="reserveBelowTarget"')
  })

  it('puts the scheduler’s verdict plainly beside the server’s reading', () => {
    const pool = liveAttention().find((a) => a.rule === 'reserveBelowTarget')!
    expect(attentionDetail(pool)).toBe('Its 2 sandboxes can’t be scheduled. No sandbox is ready in reserve, so a new claim can’t take a warm one and waits for a sandbox to start. Scheduler: all nodes have reached their Pod limit.')
  })

  it('links "See in Issues" only on rows Radar’s Issues list too', () => {
    const html = overview(liveWorkspace())
    expect(row(html, 'Pod failed')).toContain('See in Issues')
    expect(row(html, 'Warm reserve below target')).not.toContain('See in Issues')
  })

  it('says a relation is not found only where the server read its kind in full, else not read', () => {
    const absent = row(overview(liveWorkspace()), 'Warm pool not found')
    expect(absent).toMatch(/no-such-pool<\/span> <span[^>]*>not found/)
    const claim = liveAttention().find((a) => a.rule === 'cannotBind')!
    const unread: AgentSandboxAttention = { ...claim, relations: [{ ...claim.relations![0], presence: 'unread' }] }
    const html = row(overview(liveWorkspace({ attention: [unread], attentionTotal: 1 })), 'Warm pool not found')
    expect(html).toMatch(/no-such-pool<\/span> <span[^>]*>not read/)
    // A present relation is a link.
    expect(row(overview(liveWorkspace()), 'Warm reserve below target')).toContain('>tmpl</button>')
  })

  it('branches on the rule: a slow state says how long and that Issues raise it later; a stalled one is the Issue', () => {
    const base = liveAttention().find((a) => a.id.endsWith('/bad-image'))!
    const slow: AgentSandboxAttention = { ...base, rule: 'slow', severity: 'warning', title: 'Still starting', reading: 'Pod exists with phase: Pending', rawDetail: undefined, since: minutesAgo(12), issue: false }
    expect(attentionTitle(slow)).toBe('Still starting')
    expect(attentionAge(slow)?.text).toBe('≤12m')
    expect(attentionDetail(slow)).toBe('Pod exists with phase: Pending Radar’s Issues raise it after 30 minutes.')
    const stalled: AgentSandboxAttention = { ...slow, rule: 'stalled', severity: 'alert', since: minutesAgo(45), issue: true }
    expect(attentionAge(stalled)?.text).toBe('≤45m')
    expect(attentionDetail(stalled)).toBe('Pod exists with phase: Pending')
    // Any row with a known start says how long; one without says nothing.
    // A ceiling, said as one: "≤1h", with why on hover. None without since, never the creation time.
    const aged = attentionAge({ ...base, since: minutesAgo(90) })
    expect(aged?.text).toBe('≤1h')
    expect(aged?.tip).toContain('at most 1h ago')
    expect(attentionAge(base)).toBeUndefined()
    // The controller's Available=False is a real flip: exact.
    expect(attentionAge({ ...base, rule: 'controllerNotRunning', since: minutesAgo(20) })?.text).toBe('20m')
  })

  it('shows each row compactly: title, one line, the next step; the reading and the source’s words fold under Details', () => {
    const html = overview(liveWorkspace())
    const pool = row(html, 'Warm reserve below target')
    const at = (t: string) => {
      const i = pool.indexOf(t)
      expect(i, t).toBeGreaterThanOrEqual(0)
      return i
    }
    // Visible, in order: the subject, what it holds up, its one link, the next step.
    expect(at('SandboxWarmPool')).toBeLessThan(at('no claim waits on it'))
    expect(at('no claim waits on it')).toBeLessThan(at('Show the 2 sandboxes that aren’t ready'))
    expect(at('Show the 2 sandboxes that aren’t ready')).toBeLessThan(at('Next step: '))
    // Folded: Radar's fuller reading, the sandboxes it names, the scheduler's own words.
    expect(at('Next step: ')).toBeLessThan(at('>Details<'))
    expect(at('>Details<')).toBeLessThan(at('Its 2 sandboxes can’t be scheduled.'))
    expect(at('>Details<')).toBeLessThan(at('Not ready:'))
    expect(at('>Details<')).toBeLessThan(at('0/4 nodes are available'))
    // One link per row: a pool's own sandboxes, not Issues as well.
    expect(pool).not.toContain('See in Issues')
    // The controller's raw message folds too.
    const image = row(html, 'Image can’t be pulled')
    expect(image.indexOf('>Details<')).toBeLessThan(image.indexOf('is waiting: ErrImagePull'))
    expect(image.indexOf('See in Issues')).toBeLessThan(image.indexOf('Next step: '))
  })

  it('says how long a state has lasted as a ceiling, in words', () => {
    const base = liveAttention().find((a) => a.id.endsWith('/bad-image'))!
    const html = overview(liveWorkspace({ attention: [{ ...base, since: minutesAgo(90) }], attentionTotal: 1 }))
    expect(html).toContain('began ≤1h ago')
  })

  it('says how many claims an empty reserve leaves without a warm sandbox, and only what the server counted', () => {
    const pool = liveAttention().find((a) => a.rule === 'reserveBelowTarget')!
    const one = (w: AgentSandboxAttention['waitingClaims']) => row(overview(liveWorkspace({ attention: [{ ...pool, waitingClaims: w }], attentionTotal: 1 })), 'Warm reserve below target')
    expect(one({ count: 2, lowerBound: false })).toContain('2 claims can’t take a warm sandbox')
    expect(one({ count: 1, lowerBound: true })).toContain('at least 1 claim can’t take a warm sandbox')
    // Not read: nothing said, never "no claim".
    const unread = one(undefined)
    expect(unread).not.toContain('can’t take a warm sandbox')
    expect(unread).not.toContain('no claim waits')
    // From the claim's side: the empty pool it waits on, linked.
    const claim: AgentSandboxAttention = {
      id: 'claim:radar-agents-live/a', category: 'claim', rule: 'stalled', severity: 'alert', title: 'Still binding', nextStep: 'Check the SandboxClaim’s Pod and its events for what it waits on',
      subject: { kind: 'SandboxClaim', group: 'extensions.agents.x-k8s.io', namespace: 'radar-agents-live', name: 'a' },
      relations: [{ role: 'warmPool', ref: pool.subject, presence: 'present' }], emptyPool: pool.subject, issue: true,
    }
    const html = row(overview(liveWorkspace({ attention: [claim], attentionTotal: 1 })), 'Still binding')
    expect(html).toMatch(/can’t take a warm sandbox: warm pool <button[^>]*>warm<\/button> has none ready/)
    // The pool is named once, not again as a relation under Details.
    expect(html.match(/>warm<\/button>/g)).toHaveLength(1)
  })

  it('keeps what limits Radar’s reading beside the title, never under Details', () => {
    const claim = liveAttention().find((a) => a.rule === 'cannotBind')!
    const caveat = 'Radar finds SandboxWarmPool no-such-pool in radar-agents-live, so the controller’s report may predate it.'
    const html = row(overview(liveWorkspace({ attention: [{ ...claim, caveat }], attentionTotal: 1 })), 'Warm pool not found')
    expect(html.indexOf(caveat)).toBeGreaterThan(0)
    expect(html.indexOf(caveat)).toBeLessThan(html.indexOf('Next step: '))
    expect(html.indexOf(caveat)).toBeLessThan(html.indexOf('>Details<'))
  })

  it('has a blocked suspension’s Pod owner checked first, with the command as a copyable chip in the step', () => {
    const html = row(overview(liveWorkspace()), 'Suspension blocked')
    const cmd = "kubectl get pod foreign -n radar-agents-live -o jsonpath='{.metadata.ownerReferences}'"
    expect(html).toContain('Copy command')
    expect(html.replace(/<[^>]+>/g, '')).toContain(`Next step: Check who owns Pod foreign ${cmd}`)
    expect(html.replace(/<[^>]+>/g, '')).toContain(' before removing it, or recreate the Sandbox under another name')
    // The parentheses the text carries for plain readers go with the chip.
    expect(html.replace(/<[^>]+>/g, '')).not.toContain(`(${cmd})`)
  })

  it('says nothing needs attention only when the server read everything', () => {
    expect(overview(emptyWorkspace())).toContain(NOTHING_NEEDS_ATTENTION)
    expect(overview(emptyWorkspace({ attentionComplete: false }))).toContain(NOTHING_IN_WHAT_WAS_READ)
  })

  it('says when it shows only the first rows the server sent', () => {
    expect(overview(liveWorkspace({ attentionTotal: 240 }))).toContain('Showing 5 of 240.')
  })

  it('shows warm pools with template presence and claims served, unknown where claims were not read', () => {
    const html = overview(liveWorkspace())
    const table = html.slice(html.indexOf('>Warm pools</h2>'))
    expect(table).toContain('0/2')
    // The server's reserveBelowTarget row names the scheduler: the pool can't schedule, not just warming.
    expect(table).toContain('>Can’t schedule<')
    expect(table).not.toContain('>Warming<')
    const refilling = overview(liveWorkspace({ attention: liveAttention().filter((a) => a.rule !== 'reserveBelowTarget') }))
    expect(refilling.slice(refilling.indexOf('>Warm pools</h2>'))).toContain('>Warming<')
    expect(table).toContain('>tmpl</button>')
    expect(table).toMatch(/>1<\/span><div[^>]*>0 bound/)
    const w = liveWorkspace().warmPools[0]
    const unread = overview(liveWorkspace({ warmPools: [{ ...w, claims: undefined, templatePresence: 'unread' }] }))
    const t2 = unread.slice(unread.indexOf('>Warm pools</h2>'))
    expect(t2).toContain('unknown')
    expect(t2).toContain('not read')
    expect(t2).not.toContain('not found')
    // A pool that names no template says so, not a dash.
    const bare = overview(liveWorkspace({ warmPools: [{ ...w, template: undefined, templatePresence: undefined }] }))
    expect(bare.slice(bare.indexOf('>Warm pools</h2>'))).toContain('no template')
  })

  it('names what expired this week in one line, and nothing when none did', () => {
    const html = overview(liveWorkspace())
    const note = html.slice(html.indexOf('Expired this week'), html.indexOf('Needs attention'))
    for (const name of ['running', 'expired', 'claim-ok']) expect(note).toContain(`>${name}</button>`)
    expect(overview(emptyWorkspace())).not.toContain('Expired this week')
  })

  it('shows the namespace filter the server applied as a chip', () => {
    expect(overview(liveWorkspace())).toContain('Namespace: radar-agents-live')
    expect(overview(liveWorkspace({ namespaces: [] }))).not.toContain('Namespace:')
  })
})

describe('Agent Sandbox sidebar badges', () => {
  it('take the server’s counts: ≥ for a lower bound, the unknown dash for a lower-bound zero', () => {
    expect(sidebarCount({ count: 3, lowerBound: false }, ['sandbox', 'sandboxes'])).toEqual({ count: 3, lowerBound: false, title: '3 sandboxes need attention.' })
    expect(sidebarCount({ count: 2, lowerBound: true }, ['claim', 'claims']).title).toBe('At least 2 claims need attention. Some couldn’t be read, so there may be more.')
    expect(sidebarCount({ count: null, lowerBound: true }, ['warm pool', 'warm pools'])).toMatchObject({ count: null, lowerBound: true })
    expect(sidebarCount({ count: 1, lowerBound: false }, ['row', 'rows'], true).title).toContain('controller’s row')
  })
})
