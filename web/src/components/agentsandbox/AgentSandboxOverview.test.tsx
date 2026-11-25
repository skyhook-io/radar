import type { ComponentProps, ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, onTestFinished, vi } from 'vitest'

const pods = vi.hoisted(() => ({ verified: true }))
vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useResourceWithRelationships: (_kind: string, namespace: string, name: string) => ({
    data: name && pods.verified ? { relationships: { children: [{ kind: 'Pod', namespace, name: `${name}-owned-pod` }] } } : undefined,
    isLoading: false,
  }),
}))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useNamespacedCapabilities: () => ({ canViewLogs: true, logsUnknown: false }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

import { RadarFeatureUnsupportedError } from '../../api/radarFeatures'
import type { AgentSandboxAttention, AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'
import { AgentSandboxOverview, NOTHING_IN_WHAT_WAS_READ, NOTHING_NEEDS_ATTENTION, overviewFacts } from './AgentSandboxOverview'
import { AgentSandboxWorkspaceGate, attentionAge, attentionDetail, attentionTitle, ceilingDuration, sidebarCount } from './workspace'
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
  it('shows the intro only when the workspace is empty, and leads populated workspaces with Needs attention above At a glance', () => {
    const empty = overview(emptyWorkspace())
    expect(empty).toContain('Agent Sandbox gives each agent its own sandboxed Pod')
    const populated = overview(liveWorkspace())
    expect(populated).not.toContain('Agent Sandbox gives each agent its own sandboxed Pod')
    expect(populated.indexOf('Needs attention')).toBeLessThan(populated.indexOf('At a glance'))
    // Full warm-pool table lives on the Warm pools list, not duplicated on Overview.
    expect(populated).not.toContain('<table')
  })

  it('summarizes Controller, Sandboxes, Claims, and Warm reserve in framed At a glance rows', () => {
    const data = liveWorkspace()
    expect(fact(data, 'Controller')).toBe('Running (1/1 ready)')
    expect(fact(data, 'Sandboxes')).toBe('2 failed · 2 can’t schedule · 1 suspension blocked')
    expect(fact(data, 'Claims')).toBe('1 waiting · 0 bound')
    expect(fact(data, 'Warm reserve')).toBe('0 of 2 ready in 1 pool')
    const html = overview(data)
    expect(html).toContain('Deployment →')
    expect(html).toContain('cluster-level check, outside namespace filter')
    expect(html).toContain('Sandboxes →')
    expect(html).toContain('Claims →')
    expect(html).toContain('Warm pools →')
    expect(html).toContain('3 sandboxes and claims expired in the last 7 days')
  })

  it('marks counts over a partial read with ≥, and a denied or left-out kind as unknown naming the missing permission, never 0', () => {
    const live = liveWorkspace()
    const partial = liveWorkspace({ counts: { ...live.counts, sandboxes: { ...live.counts.sandboxes!, lowerBound: true } } })
    expect(fact(partial, 'Sandboxes')).toBe('≥2 failed · ≥2 can’t schedule · ≥1 suspension blocked')
    const leftOut = liveWorkspace({ counts: { ...live.counts, claims: undefined }, coverage: { ...live.coverage, claims: { state: 'denied' } } })
    expect(fact(leftOut, 'Claims')).toBe('Unknown — no access (missing list sandboxclaims.extensions.agents.x-k8s.io)')
    const notInstalled = liveWorkspace({ counts: { ...live.counts, warmPools: undefined }, coverage: { ...live.coverage, warmPools: { state: 'notInstalled' } } })
    expect(fact(notInstalled, 'Warm reserve')).toBe('Not installed')
    const html = overview(leftOut)
    expect(html).toContain('SandboxClaims (no access — missing list sandboxclaims.extensions.agents.x-k8s.io)')
  })

  it('keeps the reserve unknown when the controller has not reported', () => {
    const data = liveWorkspace()
    const workspace = { ...data, attention: [], counts: { ...data.counts, warmPools: { ...data.counts.warmPools!, readyLowerBound: true } } }
    expect(overviewFacts(workspace).find((f) => f.label === 'Warm reserve')).toMatchObject({
      value: 'unknown ready · target 2 in 1 pool',
      tone: 'unknown',
    })
  })

  it('reads the controller where it looked, names missing permission when denied, and never calls an unread one stopped', () => {
    const c = liveWorkspace().controller
    expect(fact(liveWorkspace({ controller: { ...c, state: 'notFound' } }), 'Controller')).toBe('Unknown — not found as agent-sandbox-system/agent-sandbox-controller')
    expect(fact(liveWorkspace({ controller: { ...c, state: 'unread', code: 'user_forbidden' } }), 'Controller')).toBe('Unknown — no access (missing get deployments in namespace agent-sandbox-system)')
    const unread = (code: string) => fact(liveWorkspace({ controller: { state: 'unread', namespace: c.namespace, name: c.name, code } }), 'Controller')
    expect(unread('kind_sync_pending')).toBe('Unknown — still loading')
    expect(unread('kind_sync_timed_out')).toBe('Unknown — loading is overdue')
    expect(unread('radar_kind_not_collected')).toBe('Unknown — not collected by Radar')
    expect(unread('outside_namespace_pin')).toBe('Unknown — outside Radar’s namespace scope')
    expect(fact(liveWorkspace({ controller: { ...c, state: 'scaledToZero', ready: 0, desired: 0 } }), 'Controller')).toBe('Scaled to zero')
    expect(fact(liveWorkspace({ controller: { ...c, state: 'notReady', ready: 0, since: minutesAgo(20) } }), 'Controller')).toBe('Not ready for 20m')
  })

  it('promotes a stopped controller problem callout above Needs attention without duplicating it in the list', () => {
    const c = liveWorkspace().controller
    const ctrlRow: AgentSandboxAttention = {
      id: 'controller', category: 'controller', rule: 'controllerNotRunning', reason: 'AgentSandboxControllerNotReady', severity: 'critical',
      title: 'Agent Sandbox controller not running',
      reading: 'No ready replica, so no Sandbox starts.',
      nextStep: 'Find out why the controller’s Pods aren’t ready, from their events and logs',
      subject: { kind: 'Deployment', group: 'apps', namespace: c.namespace, name: c.name },
      issue: false,
    }
    const html = overview(liveWorkspace({ controller: { ...c, state: 'notReady', ready: 0 }, attention: [ctrlRow, ...liveAttention()], attentionTotal: 6 }))
    expect(html.indexOf('Agent Sandbox controller not running')).toBeLessThan(html.indexOf('Needs attention'))
    expect(html.match(/Agent Sandbox controller not running/g)).toHaveLength(1)
  })

  it('lists every row the server sent in the server’s order, each with its next step and the source text folded', () => {
    const html = overview(liveWorkspace())
    const attention = html.slice(html.indexOf('Needs attention'), html.indexOf('At a glance'))
    expect([...attention.matchAll(/data-rule="(\w+)"/g)].map((m) => m[1])).toEqual(liveAttention().map((a) => a.rule))
    const reversed = liveAttention().reverse()
    const html2 = overview(liveWorkspace({ attention: reversed }))
    expect([...html2.matchAll(/data-rule="(\w+)"/g)].map((m) => m[1])).toEqual(reversed.map((a) => a.rule))
    expect(attention.match(/data-rule=/g)).toHaveLength(5)
    expect(attention).toContain('Controller’s message')
    expect(attention).toContain('Scheduler’s message')
    expect(attention).toContain('data-rule="reserveBelowTarget"')
  })

  it('puts the scheduler’s verdict plainly beside the server’s reading', () => {
    const pool = liveAttention().find((a) => a.rule === 'reserveBelowTarget')!
    expect(attentionDetail(pool)).toBe('Its 2 sandboxes can’t be scheduled. No sandbox is ready in reserve, so a new claim waits for one to finish starting. Scheduler: all nodes have reached their Pod limit.')
  })

  it('links Pod logs directly on failed Pods and "See in Issues" only on rows Radar’s Issues list too', () => {
    const html = overview(liveWorkspace())
    const failedRow = row(html, 'Pod failed')
    expect(failedRow).toContain('failed-owned-pod')
    expect(failedRow).toContain('Pod logs →')
    expect(failedRow).toContain('See in Issues')
    expect(row(html, 'Warm reserve below target')).not.toContain('See in Issues')
  })

  it('withholds the ordinary Pod and logs links when the owner reference could not be verified', () => {
    pods.verified = false
    onTestFinished(() => { pods.verified = true })
    const html = overview(liveWorkspace())
    expect(row(html, 'Pod failed')).not.toContain('Pod logs')
    expect(row(html, 'Suspension blocked')).toContain('Conflicting Pod')
  })

  it('says a relation is not found only where the server read its kind in full, else not read', () => {
    const absent = row(overview(liveWorkspace()), 'Warm pool not found')
    expect(absent).toMatch(/no-such-pool<\/span> <span[^>]*>not found/)
    const claim = liveAttention().find((a) => a.rule === 'cannotBind')!
    const unread: AgentSandboxAttention = { ...claim, relations: [{ ...claim.relations![0], presence: 'unread' }] }
    const html = row(overview(liveWorkspace({ attention: [unread], attentionTotal: 1 })), 'Warm pool not found')
    expect(html).toMatch(/no-such-pool<\/span> <span[^>]*>not read/)
    expect(row(overview(liveWorkspace()), 'Warm reserve below target')).toContain('>tmpl</button>')
  })

  it('branches on the rule and renders plain-English "up to" ceilings instead of ≤', () => {
    vi.useFakeTimers({ toFake: ['Date'], now: Date.parse('2026-11-18T12:00:00Z') })
    onTestFinished(() => void vi.useRealTimers())
    const base = liveAttention().find((a) => a.id.endsWith('/bad-image'))!
    const slow: AgentSandboxAttention = { ...base, rule: 'slow', severity: 'warning', title: 'Still starting', reading: 'Pod exists with phase: Pending', rawDetail: undefined, since: minutesAgo(12), issue: false }
    expect(attentionTitle(slow)).toBe('Still starting')
    expect(attentionAge(slow)?.text).toBe('up to 12m')
    expect(attentionDetail(slow)).toBe('Pod exists with phase: Pending Radar’s Issues raise it after 30 minutes.')
    const stalled: AgentSandboxAttention = { ...slow, rule: 'stalled', severity: 'alert', since: minutesAgo(45), issue: true }
    expect(attentionAge(stalled)?.text).toBe('up to 45m')
    expect(attentionDetail(stalled)).toBe('Pod exists with phase: Pending')
    const aged = attentionAge({ ...base, since: minutesAgo(90) })
    expect(aged?.text).toBe('up to 1h 30m')
    expect(aged?.tip).toContain('up to 1h 30m ago')
    expect(attentionAge(base)).toBeUndefined()
    expect(attentionAge({ ...base, rule: 'controllerNotRunning', since: minutesAgo(20) })?.text).toBe('20m')
  })

  it('rounds an age ceiling up, so the bound never understates', () => {
    const s = 1000
    const m = 60 * s
    const h = 60 * m
    expect(ceilingDuration(12 * m)).toBe('12m')
    expect(ceilingDuration(12 * m + 1)).toBe('13m')
    expect(ceilingDuration(59 * m + 30 * s)).toBe('1h')
    expect(ceilingDuration(61 * m + 10 * s)).toBe('1h 2m')
    expect(ceilingDuration(2 * h)).toBe('2h')
    expect(ceilingDuration(25 * h + 1)).toBe('1d 2h')
    expect(ceilingDuration(24 * h)).toBe('1d')
    expect(ceilingDuration(500)).toBe('1s')
  })

  it('keeps the pool cause on the first view and leaves the remedy and raw evidence under Details', () => {
    const html = overview(liveWorkspace())
    const pool = row(html, 'Warm reserve below target')
    const atPos = (t: string) => {
      const i = pool.indexOf(t)
      expect(i, t).toBeGreaterThanOrEqual(0)
      return i
    }
    expect(atPos('SandboxWarmPool')).toBeLessThan(atPos('no claim waits on it'))
    expect(atPos('no claim waits on it')).toBeLessThan(atPos('Show all members that aren’t ready'))
    expect(atPos('Its 2 sandboxes can’t be scheduled.')).toBeLessThan(atPos('>Details<'))
    expect(atPos('>Details<')).toBeLessThan(atPos('Next step: '))
    expect(atPos('>Details<')).toBeLessThan(atPos('Not ready:'))
    expect(atPos('>Details<')).toBeLessThan(atPos('0/4 nodes are available'))
    expect(pool).not.toContain('See in Issues')
    const image = row(html, 'Image can’t be pulled')
    expect(image.indexOf('>Details<')).toBeLessThan(image.indexOf('is waiting: ErrImagePull'))
    expect(image).toContain('Check the image name and the registry credentials')
  })

  it('says how long a state has lasted as "up to X ago", never "≤X"', () => {
    vi.useFakeTimers({ toFake: ['Date'], now: Date.parse('2026-11-18T12:00:00Z') })
    onTestFinished(() => void vi.useRealTimers())
    const base = liveAttention().find((a) => a.id.endsWith('/bad-image'))!
    const html = overview(liveWorkspace({ attention: [{ ...base, since: minutesAgo(90) }], attentionTotal: 1 }))
    expect(html).toContain('up to 1h 30m ago')
    expect(html).not.toContain('≤1h 30m')
  })

  it('says how many claims an empty reserve leaves without a warm sandbox, and only what the server counted', () => {
    const pool = liveAttention().find((a) => a.rule === 'reserveBelowTarget')!
    const one = (w: AgentSandboxAttention['waitingClaims']) => row(overview(liveWorkspace({ attention: [{ ...pool, waitingClaims: w }], attentionTotal: 1 })), 'Warm reserve below target')
    expect(one({ count: 2, lowerBound: false })).toContain('2 claims waiting with no warm sandbox ready')
    expect(one({ count: 1, lowerBound: true })).toContain('at least 1 claim waiting with no warm sandbox ready')
    const unread = one(undefined)
    expect(unread).not.toContain('waiting with no warm sandbox ready')
    expect(unread).not.toContain('no claim waits')
    const claim: AgentSandboxAttention = {
      id: 'claim:radar-agents-live/a', category: 'claim', rule: 'stalled', severity: 'alert', title: 'Still binding', nextStep: 'Check the SandboxClaim’s Pod and its events for what it waits on',
      subject: { kind: 'SandboxClaim', group: 'extensions.agents.x-k8s.io', namespace: 'radar-agents-live', name: 'a' },
      relations: [{ role: 'warmPool', ref: pool.subject, presence: 'present' }], emptyPool: pool.subject, issue: true,
    }
    const html = row(overview(liveWorkspace({ attention: [claim], attentionTotal: 1 })), 'Still binding')
    expect(html).toMatch(/waiting: warm pool <button[^>]*>warm<\/button> has no sandbox ready/)
    expect(html.match(/>warm<\/button>/g)).toHaveLength(1)
  })

  it('keeps what limits Radar’s reading beside the title, never under Details', () => {
    const claim = liveAttention().find((a) => a.rule === 'cannotBind')!
    const caveat = 'Radar finds SandboxWarmPool no-such-pool in radar-agents-live, so the controller’s report may predate it.'
    const html = row(overview(liveWorkspace({ attention: [{ ...claim, caveat }], attentionTotal: 1 })), 'Warm pool not found')
    expect(html.indexOf(caveat)).toBeGreaterThan(0)
    expect(html.indexOf(caveat)).toBeLessThan(html.indexOf('Create SandboxWarmPool'))
    expect(html.indexOf(caveat)).toBeLessThan(html.indexOf('>Details<'))
  })

  it('links the conflicting Pod directly on Suspension blocked and folds the kubectl command under Details', () => {
    const html = row(overview(liveWorkspace()), 'Suspension blocked')
    const cmd = "kubectl get pod foreign -n radar-agents-live -o jsonpath='{.metadata.ownerReferences}'"
    const plain = html.replace(/<[^>]+>/g, '')
    expect(html).toContain('Conflicting Pod')
    expect(plain).toContain('Next step: Check who owns Pod foreign before removing it, or recreate the Sandbox under another name')
    expect(plain).not.toContain(`(${cmd})`)
    // CommandChip lives inside Details, after the Details toggle.
    expect(html.indexOf('>Details<')).toBeLessThan(html.indexOf('data-command-line'))
    expect(html.slice(html.indexOf('data-command-line'))).toContain(cmd)
  })

  it('says nothing needs attention only when the server read everything', () => {
    expect(overview(emptyWorkspace())).toContain(NOTHING_NEEDS_ATTENTION)
    expect(overview(emptyWorkspace({ attentionComplete: false }))).toContain(NOTHING_IN_WHAT_WAS_READ)
  })

  it('says when it shows only the first rows the server sent', () => {
    expect(overview(liveWorkspace({ attentionTotal: 240 }))).toContain('Showing 5 of 240 findings.')
  })

  it('links recent expirations into the Sandboxes inventory and shows nothing when none expired', () => {
    expect(overview(liveWorkspace())).toContain('Recently expired sandboxes →')
    expect(overview(liveWorkspace())).toContain('Recently expired claims →')
    expect(overview(emptyWorkspace())).not.toContain('Recently expired')
  })

  it('shows the namespace filter the server applied as a chip and subtitle', () => {
    expect(overview(liveWorkspace())).toContain('Namespace: radar-agents-live')
    expect(overview(liveWorkspace({ namespaces: [] }))).toContain('All namespaces')
  })
})

describe('Agent Sandbox sidebar badges', () => {
  it('take the server’s counts: ≥ for a lower bound, the unknown dash for a lower-bound zero', () => {
    expect(sidebarCount({ count: 3, lowerBound: false }, ['sandbox', 'sandboxes'])).toEqual({ count: 3, lowerBound: false, title: '3 sandboxes need attention.' })
    expect(sidebarCount({ count: 2, lowerBound: true }, ['claim', 'claims']).title).toBe('At least 2 claims need attention. Some couldn’t be read, so there may be more.')
    expect(sidebarCount({ count: null, lowerBound: true }, ['warm pool', 'warm pools'])).toMatchObject({ count: null, lowerBound: true })
    expect(sidebarCount({ count: 1, lowerBound: false }, ['object', 'objects']).title).toBe('1 object needs attention.')
  })
})
