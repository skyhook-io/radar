import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

import { getSandboxStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { AgentSandboxLists } from './data'
import { ClaimsScreen, SandboxesScreen, WarmPoolsScreen, filterSandboxes, poolNotReadyPath, requestedMode, stateMessage } from './AgentSandboxTables'
import { AgentSandboxGate } from './shared'
import { NS, PARTIAL, claim, emptyData, liveData, liveSandboxes, read, sandbox, warmPool } from './agentsandbox.fixtures'
import { liveWorkspace } from './workspace.fixtures'
import { serverReadings } from './labels'

// The kind lists read the resource list endpoint, with its coverage; the
// Overview's tests are in AgentSandboxOverview.test.tsx.

// renderToStaticMarkup escapes apostrophes; compare against the text a reader sees.
const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/<!-- -->/g, '')
const DENIED = { state: 'failed', message: 'forbidden', denied: true } as const

function page(node: ReactNode, url = '/') {
  return text(renderToStaticMarkup(<MemoryRouter initialEntries={[url]}>{node}</MemoryRouter>))
}

function warmPoolWithoutTemplate() {
  const pool = warmPool('bare')
  delete pool.spec.sandboxTemplateRef
  return pool
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

  it('says a claim’s missing warm pool once, as its state, with the pool named in its column', () => {
    const html = page(<ClaimsScreen {...screenProps(liveData())} />)
    expect(html.match(/[Nn]ot found/g)).toEqual(['not found'])
    expect(html).toContain('>Warm pool not found<')
    expect(html).not.toContain('>WarmPoolNotFound<')
    // The controller's sentence is in the drawer, not under the state.
    expect(html).not.toContain('SandboxWarmPool "no-such-pool" not found')
    // Named, not linked; the pool it has is linked.
    expect(html).toMatch(/>no-such-pool<\/span>/)
    expect(html).not.toContain('>no-such-pool</button>')
    expect(html).toContain('>warm</button>')
  })

  it('still says not found where only Radar’s read says the pool is missing', () => {
    // The controller hasn't said why this claim waits: the column is the only place the missing pool shows.
    const waiting = claim('waiting', { pool: 'gone-pool', reason: 'SandboxNotReady', message: 'waiting for a sandbox' })
    const html = page(<ClaimsScreen {...screenProps(liveData({ claims: [waiting] }))} />)
    expect(html).toMatch(/gone-pool(<\/span>)+<div[^>]*>not found/)
  })

  it('says a relation it could not read is not read, never not found', () => {
    // Warm pools read only in another namespace: the claim's pool is unread here.
    const partial = read([], { status: 'partial', scope: { type: 'all_namespaces' }, read: { type: 'namespaces', namespaces: { count: 1, names: ['other'] } }, gaps: [{ code: 'user_forbidden', namespaces: { count: 1, names: [NS] } }] })
    const claims = page(<ClaimsScreen {...screenProps(liveData({ warmPools: partial }))} />)
    expect(claims).toMatch(/no-such-pool<\/span>.*?not read/)
    expect(claims).not.toMatch(/no-such-pool(<\/span>)+<div[^>]*>not found/)
    expect(claims).toContain('>Warm pool not found<')
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

  it('says why a warm pool can’t schedule in the Overview’s plain words, and links the sandboxes it holds back', () => {
    const html = page(<WarmPoolsScreen {...screenProps(liveData())} server={serverReadings(liveWorkspace())} />)
    expect(html).toMatch(/Can’t schedule<.*?>All nodes have reached their Pod limit</)
    // The scheduler's own message is on hover and in the drawer, not in the row.
    expect(html).not.toContain('Too many pods')
    expect(html).toMatch(/<button[^>]*>2 sandboxes not ready<\/button>/)
    // Without the server's row there is no reason to give and nothing to count.
    const bare = page(<WarmPoolsScreen {...screenProps(liveData())} />)
    expect(bare).not.toContain('Pod limit')
    expect(bare).not.toContain('not ready</button>')
  })

  it('narrows the Sandboxes list to one pool’s sandboxes that aren’t ready, as the pool’s link asks', () => {
    expect(poolNotReadyPath(NS, 'warm')).toBe(`/resources/sandboxes?apiGroup=agents.x-k8s.io&pool=${encodeURIComponent(`${NS}/warm`)}&state=notReady`)
    const all = read(liveSandboxes())
    const names = (r: ReturnType<typeof filterSandboxes>) => (r.state === 'read' ? r.items.map((o) => o.metadata.name).sort() : [])
    expect(names(filterSandboxes(all, `${NS}/warm`, 'notReady'))).toEqual(['warm-4xmkm', 'warm-nd2zw'])
    expect(names(filterSandboxes(all, `${NS}/other`, 'notReady'))).toEqual([])
    expect(filterSandboxes(all, '', '')).toBe(all)
    const html = page(<SandboxesScreen {...screenProps(liveData())} />, `/resources/sandboxes?pool=${encodeURIComponent(`${NS}/warm`)}&state=notReady`)
    expect(html).toContain('Warm pool: warm')
    expect(html).toContain('Not ready')
    expect(html).toContain('warm-4xmkm')
    expect(html).not.toContain('bad-image')
    const none = page(<SandboxesScreen {...screenProps(liveData())} />, `/resources/sandboxes?pool=${encodeURIComponent(`${NS}/other`)}`)
    expect(none).toContain('No Sandboxes match these filters.')
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
    const stalled = { unschedulable: new Set<string>(), tones: new Map([[`Sandbox/${NS}/slow`, 'alert' as const]]), rows: new Map() }
    expect(page(<SandboxesScreen {...screenProps(data)} server={stalled} />)).toMatch(/class="[^"]*orange[^"]*"[^>]*>Provisioning</)
  })

  it('says what an empty owner, expiry, warm pool or template means, and only where Radar knows', () => {
    // No owner and every claim here read: standalone. No shutdownTime: no expiry.
    const sandboxes = page(<SandboxesScreen {...screenProps(liveData())} />)
    const badImage = sandboxes.slice(sandboxes.indexOf('>bad-image<'), sandboxes.indexOf('</tr>', sandboxes.indexOf('>bad-image<')))
    expect(badImage).toContain('standalone')
    expect(badImage).toContain('no expiry')
    expect(badImage).not.toContain('>–<')
    // Claims not read: no warm pool owns it, and a claim may still name it.
    const unread = page(<SandboxesScreen {...screenProps(liveData({ claims: DENIED }))} />)
    expect(unread).toContain('no warm pool; claims not read')
    expect(unread).not.toContain('standalone')
    // A claimed Sandbox with no shutdownTime of its own may still end with its claim.
    const claimOwner = [{ apiVersion: 'extensions.agents.x-k8s.io/v1beta1', kind: 'SandboxClaim', name: 'c', uid: 'u', controller: true }]
    const claimed = page(<SandboxesScreen {...screenProps(liveData({ sandboxes: [sandbox('claimed', { ownerReferences: claimOwner })] }))} />)
    expect(claimed).toContain('none of its own')
    expect(claimed).not.toContain('no expiry')
    // Another kind owns it: named, not called standalone.
    const deploy = [{ apiVersion: 'apps/v1', kind: 'Deployment', name: 'runner', uid: 'u', controller: true }]
    const other = page(<SandboxesScreen {...screenProps(liveData({ sandboxes: [sandbox('owned', { ownerReferences: deploy })] }))} />)
    expect(other).toContain('>runner<')
    expect(other).toContain('Deployment')
    expect(other).not.toContain('standalone')

    const noPool = claim('direct')
    delete noPool.spec.warmPoolRef
    expect(page(<ClaimsScreen {...screenProps(liveData({ claims: [noPool] }))} />)).toContain('no warm pool')
    const bare = warmPoolWithoutTemplate()
    expect(page(<WarmPoolsScreen {...screenProps(liveData({ warmPools: [bare] }))} />)).toContain('no template')
  })

  it('says an empty list is unknown when the kind could not be read', () => {
    expect(page(<SandboxesScreen {...screenProps(liveData({ sandboxes: DENIED }))} />)).toContain('Unknown: no access to Sandboxes here.')
    expect(page(<ClaimsScreen {...screenProps(emptyData())} />)).toContain('No SandboxClaims found in the requested scope.')
    expect(page(<WarmPoolsScreen {...screenProps(emptyData({ warmPools: read([], PARTIAL) }))} />)).toContain('No SandboxWarmPools found in the namespaces Radar could read.')
  })
})
