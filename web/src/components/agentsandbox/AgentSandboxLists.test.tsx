import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

import { getSandboxStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { AgentSandboxLists } from './data'
import { ClaimsScreen, SandboxesScreen, WarmPoolsScreen, requestedMode, stateMessage } from './AgentSandboxTables'
import { AgentSandboxGate } from './shared'
import { NS, PARTIAL, emptyData, liveData, liveSandboxes, read, sandbox } from './agentsandbox.fixtures'
import { liveWorkspace } from './workspace.fixtures'
import { serverReadings } from './labels'

// The kind lists read the resource list endpoint, with its coverage; the
// Overview's tests are in AgentSandboxOverview.test.tsx.

// renderToStaticMarkup escapes apostrophes; compare against the text a reader sees.
const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/<!-- -->/g, '')
const DENIED = { state: 'failed', message: 'forbidden', denied: true } as const

function page(node: ReactNode) {
  return text(renderToStaticMarkup(<MemoryRouter>{node}</MemoryRouter>))
}

const screenProps = (data: AgentSandboxLists, namespaces: string[] = []) => ({ data, namespaces, onClearNamespaces: () => {}, onInspect: () => {}, inspected: null })

function gate(data: AgentSandboxLists, opts: Partial<{ discoveryPending: boolean; discovered: boolean }> = {}) {
  return page(
    <AgentSandboxGate data={data} keys={['sandboxes', 'claims', 'warmPools']} discoveryPending={opts.discoveryPending ?? false} discovered={opts.discovered ?? true} isFetching={false} refetch={() => {}} refreshQueries={[]}>
      <div>screen</div>
    </AgentSandboxGate>,
  )
}

describe('Agent Sandbox kind list gate', () => {
  it('waits for discovery and for a first answer, then shows the screen with what is still loading named on it', () => {
    expect(gate(liveData(), { discoveryPending: true })).toContain('Loading Agent Sandbox')
    expect(gate(liveData({ sandboxes: { state: 'loading' }, claims: { state: 'loading' }, warmPools: { state: 'loading' } }))).toContain('Loading Agent Sandbox')
    expect(gate(liveData({ claims: { state: 'loading' } }))).toContain('screen')
  })

  it('says when Agent Sandbox is not installed', () => {
    expect(gate(liveData(), { discovered: false })).toContain('Agent Sandbox is not installed')
  })

  it('says a kind this Agent Sandbox version does not serve, rather than that Agent Sandbox is missing', () => {
    const html = gate(liveData({ sandboxes: { state: 'notServed' }, claims: { state: 'notServed' }, warmPools: { state: 'notServed' } }))
    expect(html).toContain('Not served by this Agent Sandbox')
    expect(html).not.toContain('Agent Sandbox is not installed')
  })

  it('tells a denial from a failure, and offers a retry only for the failure', () => {
    const denied = gate(liveData({ sandboxes: DENIED, claims: DENIED, warmPools: DENIED }))
    expect(denied).toContain('No access to Agent Sandbox objects')
    expect(denied).not.toContain('Retry')
    const failed = gate(liveData({ sandboxes: { state: 'failed', message: 'boom', denied: false }, claims: DENIED, warmPools: DENIED }))
    expect(failed).toContain('Agent Sandbox data unavailable')
    expect(failed).toContain('boom')
    expect(failed).toContain('Retry')
  })
})

describe('Agent Sandbox kind lists', () => {
  it('lists sandboxes with their reading, the controller’s message, owner and expiry, failures first', () => {
    const html = page(<SandboxesScreen {...screenProps(liveData())} />)
    expect(html).toContain('Agent Sandbox')
    expect(html.indexOf('ImagePullBackOff')).toBeLessThan(html.indexOf('>Suspended<'))
    // Plain words on the badge; the controller's code stays on hover.
    for (const label of ['>Image can’t be pulled<', '>Pod failed<', '>Can’t schedule<']) expect(html).toContain(label)
    for (const raw of ['>ImagePullBackOff<', '>PodFailed<', '>Unschedulable<']) expect(html).not.toContain(raw)
    expect(html).toContain('Suspension blocked')
    expect(html).toContain('The conflicting Pod is "foreign"')
    // Narrow (no matchMedia here): the pool sits under the Sandbox's name, the age under its expiry.
    expect(html).toMatch(/warm pool <span[^>]*><button[^>]*>warm<\/button>/)
    expect(html).toMatch(/expired 1d ago.*?\dd old/)
    expect(html).not.toContain('>Claim or pool<')
    expect(html).not.toContain('>Age<')
    expect(html).toMatch(/expired 1d ago/)
    // The spec's operating mode shows only where the state hasn't caught up with it.
    expect(html).not.toContain('Mode')
    expect(html).not.toContain('requested running')
  })

  it('explains a state under its badge only when it adds something, the scheduler’s verdict in plain words', () => {
    const by = Object.fromEntries(liveSandboxes().map((s) => [s.metadata.name, stateMessage(s, getSandboxStatus(s))]))
    expect(by['warm-4xmkm']).toBe('All nodes have reached their Pod limit')
    expect(by['bad-image']).toContain('ImagePullBackOff')
    for (const name of ['failed', 'expired', 'finished', 'running', 'suspended']) expect(by[name], name).toBeUndefined()
  })

  it('says what was requested only where the state differs from it', () => {
    const suspendedAsked = { ...liveSandboxes()[1], spec: { operatingMode: 'Suspended' } }
    const resuming = { ...liveSandboxes().find((s) => s.metadata.name === 'suspended'), spec: { operatingMode: 'Running' } }
    expect(requestedMode(suspendedAsked)).toBe('suspended')
    expect(requestedMode(resuming)).toBe('running')
    for (const s of liveSandboxes()) expect(requestedMode(s), s.metadata.name).toBeUndefined()
    expect(page(<SandboxesScreen {...screenProps(liveData({ sandboxes: [suspendedAsked] }))} />)).toContain('requested suspended')
  })

  it('names a claim’s missing warm pool instead of linking it, and links the pool it has', () => {
    const html = page(<ClaimsScreen {...screenProps(liveData())} />)
    expect(html).toMatch(/no-such-pool<\/span>.*?not found/)
    expect(html).toContain('>warm</button>')
    expect(html).toContain('>Warm pool not found<')
    expect(html).not.toContain('>WarmPoolNotFound<')
  })

  it('says a relation it could not read is not read, never not found', () => {
    // Warm pools read only in another namespace: the claim's pool is unread here.
    const partial = read([], { status: 'partial', scope: { type: 'all_namespaces' }, read: { type: 'namespaces', namespaces: { count: 1, names: ['other'] } }, gaps: [{ code: 'user_forbidden', namespaces: { count: 1, names: [NS] } }] })
    const claims = page(<ClaimsScreen {...screenProps(liveData({ warmPools: partial }))} />)
    expect(claims).toMatch(/no-such-pool<\/span>.*?not read/)
    expect(claims).not.toMatch(/no-such-pool(<\/span>)+<div[^>]*>not found/)
    expect(page(<ClaimsScreen {...screenProps(liveData({ warmPools: DENIED }))} />)).toMatch(/no-such-pool(<\/span>)+<div[^>]*>not read/)
    // A pool's claims Radar couldn't read are unknown, not none.
    expect(page(<WarmPoolsScreen {...screenProps(liveData({ claims: DENIED }))} />)).toMatch(/unknown/)
  })

  it('lists warm pools with ready of desired and the claims they serve', () => {
    const html = page(<WarmPoolsScreen {...screenProps(liveData())} />)
    expect(html).toContain('0/2')
    // Without the server saying its sandboxes can't be placed, a short pool is refilling.
    expect(html).toContain('>Warming<')
  })

  it('reads a warm pool as unable to schedule only when the server says so', () => {
    const server = serverReadings(liveWorkspace())
    const html = page(<WarmPoolsScreen {...screenProps(liveData())} server={server} />)
    expect(html).toContain('>Can’t schedule<')
    expect(html).not.toContain('>Warming<')
    // Colored as its Needs attention row (alert, orange), the same as on the Overview.
    expect(html).toMatch(/class="[^"]*orange[^"]*"[^>]*>Can’t schedule</)
  })

  it('colors a row as the server’s Needs attention row about it, so a state reads the same on the Overview', () => {
    // A claim that can't bind is a warning in the reading, the row and Issues alike.
    const claims = page(<ClaimsScreen {...screenProps(liveData())} server={serverReadings(liveWorkspace())} />)
    expect(claims).toMatch(/class="[^"]*amber[^"]*"[^>]*>Warm pool not found</)
    expect(page(<ClaimsScreen {...screenProps(liveData())} />)).toMatch(/class="[^"]*amber[^"]*"[^>]*>Warm pool not found</)
    // A Sandbox still provisioning reads amber; past 30 minutes the server's row is stalled (alert), and the list follows it.
    const at = new Date(Date.now() - 45 * 60_000).toISOString()
    const slow = sandbox('slow', { conditions: [{ type: 'Ready', status: 'False', reason: 'DependenciesNotReady', message: 'Pod exists with phase: Pending', lastTransitionTime: at }] })
    const data = liveData({ sandboxes: [slow] })
    expect(page(<SandboxesScreen {...screenProps(data)} />)).toMatch(/class="[^"]*amber[^"]*"[^>]*>Provisioning</)
    const stalled = { unschedulable: new Set<string>(), tones: new Map([[`Sandbox/${NS}/slow`, 'alert' as const]]) }
    expect(page(<SandboxesScreen {...screenProps(data)} server={stalled} />)).toMatch(/class="[^"]*orange[^"]*"[^>]*>Provisioning</)
  })

  it('says an empty list is unknown when the kind could not be read', () => {
    expect(page(<SandboxesScreen {...screenProps(liveData({ sandboxes: DENIED }))} />)).toContain('Unknown: no access to Sandboxes here.')
    expect(page(<ClaimsScreen {...screenProps(emptyData())} />)).toContain('No SandboxClaims found in the requested scope.')
    expect(page(<WarmPoolsScreen {...screenProps(emptyData({ warmPools: read([], PARTIAL) }))} />)).toContain('No SandboxWarmPools found in the namespaces Radar could read.')
  })
})
