import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

import { getSandboxClaimStatus, getSandboxStatus, getSandboxWarmPoolStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { AgentSandboxLists } from './data'
import { ClaimsScreen, SandboxesScreen, WarmPoolsScreen, filterSandboxes, poolNotReadyPath, requestedMode, stateMessage } from './AgentSandboxTables'
import { AgentSandboxGate } from './shared'
import { NS, PARTIAL, claim, emptyData, liveClaims, liveData, liveSandboxes, read, sandbox, warmPool } from './agentsandbox.fixtures'
import { liveWorkspace } from './workspace.fixtures'
import { serverReadings } from './labels'
import { QueryClient } from '@tanstack/react-query'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { agentSandboxListKey } from './data'
import { readListFilters, withoutColumn } from './listFilters'
import { SANDBOX_CLAIM_KIND, SANDBOX_KIND, SANDBOX_WARM_POOL_KIND } from './routes'

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
    expect(denied).toContain('Missing permission to list sandboxes.agents.x-k8s.io')
    expect(denied).not.toContain('Retry')
    const failed = gate(liveData({ sandboxes: { state: 'failed', message: 'boom', denied: false }, claims: DENIED, warmPools: DENIED }))
    expect(failed).toContain('Agent Sandbox data unavailable')
    expect(failed).toContain('boom')
    expect(failed).toContain('Retry')
  })
})

describe('Agent Sandbox kind lists', () => {
  it('lists sandboxes with their reading, concise reason, owner, expiry, and visible Open affordance, failures first', () => {
    const html = page(<SandboxesScreen {...screenProps(liveData())} />)
    expect(html).toContain('Agent Sandbox')
    // Clickable breadcrumb back to Overview.
    expect(html).toMatch(/<button[^>]*>Agent Sandbox<\/button>/)
    // Single-namespace subtitle shown under All namespaces.
    expect(html).toContain(`in namespace ${NS}`)
    // Segments toolbar includes Needs attention, Ready, and Settled.
    expect(html).toContain('Needs attention')
    expect(html).toContain('Settled')
    expect(html).toContain('Open →')
    expect(html.indexOf('ImagePullBackOff')).toBeLessThan(html.indexOf('>Suspended<'))
    // Plain words on the badge; the controller's code stays on hover.
    for (const label of ['>Image can’t be pulled<', '>Pod failed<', '>Can’t schedule<']) expect(html).toContain(label)
    for (const raw of ['>ImagePullBackOff<', '>PodFailed<', '>Unschedulable<']) expect(html).not.toContain(raw)
    expect(html).toContain('Suspension blocked')
    expect(html).toContain('Conflicting Pod "foreign" is not owned by this Sandbox')
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

  it('explains affected rows concisely: scheduler plain words, stripped container reason, Pod failed hint, and suspension conflict', () => {
    const by = Object.fromEntries(liveSandboxes().map((s) => [s.metadata.name, stateMessage(s, getSandboxStatus(s))]))
    expect(by['warm-4xmkm']).toBe('All nodes have reached their Pod limit')
    expect(by['bad-image']).toBe('Container "agent" is waiting: ImagePullBackOff')
    expect(by['failed']).toContain('Pod exited with failure')
    expect(by['foreign']).toBe('Conflicting Pod "foreign" is not owned by this Sandbox')
    for (const name of ['expired', 'finished', 'running', 'suspended']) expect(by[name], name).toBeUndefined()
  })

  it('supports Needs attention, Ready, Settled segments and ?state=expired filtering on Sandboxes', () => {
    const attn = page(<SandboxesScreen {...screenProps(liveData())} />, '/resources/sandboxes?segment=attention')
    expect(attn).toContain('bad-image')
    expect(attn).toContain('failed')
    expect(attn).not.toContain('>suspended<')
    const settled = page(<SandboxesScreen {...screenProps(liveData())} />, '/resources/sandboxes?segment=settled')
    expect(settled).toContain('>suspended<')
    expect(settled).toContain('>finished<')
    expect(settled).not.toContain('>bad-image<')
    const expired = page(<SandboxesScreen {...screenProps(liveData())} />, '/resources/sandboxes?state=expired')
    expect(expired).toContain('>expired<')
    expect(expired).not.toContain('>suspended<')
  })

  it('matches the Sandboxes sidebar badge when a pool finding covers a member', () => {
    const workspace = liveWorkspace()
    const member = 'warm-4xmkm'
    expect(workspace.attention.some((row) => row.subject.name === member)).toBe(false)
    expect(workspace.attention.find((row) => row.rule === 'reserveBelowTarget')?.related?.refs).toContainEqual(expect.objectContaining({ name: member }))

    const html = page(<SandboxesScreen {...screenProps(liveData())} server={serverReadings(workspace)} />, '/resources/sandboxes?segment=attention')
    expect(html).toMatch(new RegExp(`Needs attention<span[^>]*>${workspace.sidebar.sandboxes.count}</span>`))
    expect(html).toContain(`>${member}<`)
    expect(html).not.toContain('>suspended<')
  })

  it('says what was requested only where the state differs from it', () => {
    const suspendedAsked = { ...liveSandboxes()[1], spec: { operatingMode: 'Suspended' } }
    const resuming = { ...liveSandboxes().find((s) => s.metadata.name === 'suspended'), spec: { operatingMode: 'Running' } }
    expect(requestedMode(suspendedAsked)).toBe('suspended')
    expect(requestedMode(resuming)).toBe('running')
    for (const s of liveSandboxes()) expect(requestedMode(s), s.metadata.name).toBeUndefined()
    expect(page(<SandboxesScreen {...screenProps(liveData({ sandboxes: [suspendedAsked] }))} />)).toContain('requested suspended')
  })

  it('shows a claim’s missing warm pool clearly in its column, and supports Claims segments and pool filter', () => {
    const html = page(<ClaimsScreen {...screenProps(liveData())} />)
    expect(html).toContain('>Warm pool not found<')
    expect(html).not.toContain('>WarmPoolNotFound<')
    expect(html).not.toContain('SandboxWarmPool "no-such-pool" not found')
    // Named, not linked, with verified missing tag; the pool it has is linked.
    expect(html).toMatch(/no-such-pool(<\/span>)+<div[^>]*><span[^>]*>not found/)
    expect(html).not.toContain('>no-such-pool</button>')
    expect(html).toContain('>warm</button>')
    expect(html).toContain('Open →')
    // Filter Claims by warm pool (return journey from Warm pools).
    const forWarm = page(<ClaimsScreen {...screenProps(liveData())} />, `/resources/sandboxclaims?pool=${encodeURIComponent(`${NS}/warm`)}`)
    expect(forWarm).toContain('claim-ok')
    expect(forWarm).not.toContain('claim-missing-pool')
    // Segment filter for Expired.
    const expired = page(<ClaimsScreen {...screenProps(liveData())} />, '/resources/sandboxclaims?segment=expired')
    expect(expired).toContain('claim-ok')
    expect(expired).not.toContain('claim-missing-pool')
  })

  it('still says not found where only Radar’s read says the pool is missing', () => {
    // The controller hasn't said why this claim waits: the column is the only place the missing pool shows.
    const waiting = claim('waiting', { pool: 'gone-pool', reason: 'SandboxNotReady', message: 'waiting for a sandbox' })
    const html = page(<ClaimsScreen {...screenProps(liveData({ claims: [waiting] }))} />)
    expect(html).toMatch(/gone-pool(<\/span>)+<div[^>]*><span[^>]*>not found/)
  })

  it('says a relation it could not read is not read, never not found', () => {
    // Warm pools read only in another namespace: the claim's pool is unread here.
    const partial = read([], { status: 'partial', scope: { type: 'all_namespaces' }, read: { type: 'namespaces', namespaces: { count: 1, names: ['other'] } }, gaps: [{ code: 'user_forbidden', namespaces: { count: 1, names: [NS] } }] })
    const claims = page(<ClaimsScreen {...screenProps(liveData({ warmPools: partial }))} />)
    expect(claims).toMatch(/no-such-pool<\/span>.*?not read/)
    expect(claims).not.toMatch(/no-such-pool(<\/span>)+<div[^>]*><span[^>]*>not found/)
    expect(claims).toContain('>Warm pool not found<')
    expect(page(<ClaimsScreen {...screenProps(liveData({ warmPools: DENIED }))} />)).toMatch(/no-such-pool(<\/span>)+<div[^>]*><span[^>]*>not read/)
    // A pool's claims Radar couldn't read are unknown, not none.
    expect(page(<WarmPoolsScreen {...screenProps(liveData({ claims: DENIED }))} />)).toMatch(/unknown/)
  })

  it('lists warm pools with ready of desired and Claims referencing / currently bound', () => {
    const html = page(<WarmPoolsScreen {...screenProps(liveData())} />)
    expect(html).toContain('0/2')
    expect(html).toContain('Claims referencing')
    expect(html).toContain('1 referencing')
    expect(html).toContain('0 currently bound · 1 expired')
    expect(html).toContain('Open →')
    // Without the server saying its reserve is below target, a short pool is refilling.
    expect(html).toContain('>Warming<')
  })

  it('uses Reserve below target as the warm pool verdict when the server reports reserveBelowTarget, with cause beneath', () => {
    const server = serverReadings(liveWorkspace())
    const html = page(<WarmPoolsScreen {...screenProps(liveData())} server={server} />)
    expect(html).toContain('>Reserve below target<')
    expect(html).not.toContain('>Warming<')
    // Colored as its Needs attention row (alert, orange), the same as on the Overview.
    expect(html).toMatch(/class="[^"]*orange[^"]*"[^>]*>Reserve below target</)
    expect(html).toContain('Can’t schedule · All nodes have reached their Pod limit')
    // The scheduler's own message is on hover and in the drawer, not in the row.
    expect(html).not.toContain('Too many pods')
    expect(html).toMatch(/<button[^>]*>Show all members that aren’t ready<\/button>/)
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

  it('applies the Resources page’s status filter, matching the text the standard table filters on', () => {
    const suspended = page(<SandboxesScreen {...screenProps(liveData())} />, `/resources/sandboxes?filters=${encodeURIComponent('status:Suspended')}`)
    expect(suspended).toContain('Status: Suspended')
    const shown = liveSandboxes().map((o) => o.metadata.name).filter((n) => suspended.includes(`>${n}<`))
    expect(shown).toEqual(liveSandboxes().filter((o) => getSandboxStatus(o).text === 'Suspended').map((o) => o.metadata.name))
    expect(shown).toEqual(['suspended'])
    const excluded = page(<SandboxesScreen {...screenProps(liveData())} />, `/resources/sandboxes?filters=${encodeURIComponent('status:exclude:Suspended')}`)
    expect(excluded).toContain('Status excludes: Suspended')
    expect(excluded).toContain('>foreign<')
    expect(excluded).not.toContain('>suspended<')

    const claimText = getSandboxClaimStatus(liveClaims()[0]).text
    const claims = page(<ClaimsScreen {...screenProps(liveData())} />, `/resources/sandboxclaims?filters=${encodeURIComponent(`status:${claimText}`)}`)
    expect(claims).toContain('claim-missing-pool')
    expect(claims).not.toContain('claim-ok')
    const cold = warmPool('cold', { uid: 'a' })
    const full = warmPool('full', { uid: 'b', readyReplicas: 2 })
    const pools = liveData({ warmPools: [cold, full] })
    const fullText = getSandboxWarmPoolStatus(full).text
    expect(fullText).not.toBe(getSandboxWarmPoolStatus(cold).text)
    const poolsHtml = page(<WarmPoolsScreen {...screenProps(pools)} />, `/resources/sandboxwarmpools?filters=${encodeURIComponent(`status:${fullText}`)}`)
    expect(poolsHtml).toContain('>full<')
    expect(poolsHtml).not.toContain('>cold<')
    expect(page(<WarmPoolsScreen {...screenProps(pools)} />, `/resources/sandboxwarmpools?filters=${encodeURIComponent('status:Nope')}`)).toContain('No SandboxWarmPools match these filters.')
  })

  it('names the Resources filters these lists can’t apply instead of dropping them', () => {
    const search = `?filters=${encodeURIComponent('status:Suspended|label%3Atier:gold')}&labels=app%3Dx&ownerKind=Deployment&ownerName=web`
    const { columns, ignored } = readListFilters(search, SANDBOX_KIND)
    expect(columns).toEqual([{ key: 'status', label: 'Status', values: ['Suspended'], exclude: false }])
    expect(ignored.map((f) => f.label)).toEqual(['label:tier: gold', 'Labels: app=x', 'Owner: Deployment/web'])
    const html = page(<SandboxesScreen {...screenProps(liveData())} />, `/resources/sandboxes${search}`)
    expect(html).toContain('Labels: app=x (not applied here)')
    expect(html).toContain('Owner: Deployment/web (not applied here)')
    // Clearing one column keeps the others.
    expect(withoutColumn(search, 'status').get('filters')).toBe('label%3Atier:gold')
    expect(withoutColumn(`?filters=status:Suspended`, 'status').has('filters')).toBe(false)
  })

  it('narrows each list by name from the Resources page’s search param', () => {
    const sandboxes = page(<SandboxesScreen {...screenProps(liveData())} />, '/resources/sandboxes?search=BAD')
    expect(sandboxes).toContain('bad-image')
    expect(sandboxes).not.toContain('foreign')
    expect(page(<SandboxesScreen {...screenProps(liveData())} />, '/resources/sandboxes?search=nothing-here')).toContain('No Sandboxes match this search.')
    const claims = page(<ClaimsScreen {...screenProps(liveData())} />, '/resources/sandboxclaims?search=ok')
    expect(claims).toContain('claim-ok')
    expect(claims).not.toContain('claim-missing-pool')
    expect(page(<ClaimsScreen {...screenProps(liveData())} />, '/resources/sandboxclaims?search=nothing-here')).toContain('No SandboxClaims match this search.')
    const data = liveData({ warmPools: [warmPool('alpha', { uid: 'a' }), warmPool('beta', { uid: 'b' })] })
    const pools = page(<WarmPoolsScreen {...screenProps(data)} />, '/resources/sandboxwarmpools?search=alp')
    expect(pools).toContain('alpha')
    expect(pools).not.toContain('beta')
    expect(page(<WarmPoolsScreen {...screenProps(data)} />, '/resources/sandboxwarmpools?search=nothing-here')).toContain('No SandboxWarmPools match this search.')
    // The box shows the search it applies.
    expect(sandboxes).toMatch(/value="BAD"/)
  })

  it('colors a row as the server’s Needs attention row about it, so a state reads the same on the Overview', () => {
    // A claim that can't bind is a warning in the reading, the row and Issues alike.
    const claims = page(<ClaimsScreen {...screenProps(liveData())} server={serverReadings(liveWorkspace())} />)
    expect(claims).toMatch(/class="[^"]*amber[^"]*"[^>]*>Warm pool not found</)
    expect(page(<ClaimsScreen {...screenProps(liveData())} />)).toMatch(/class="[^"]*amber[^"]*"[^>]*>Warm pool not found</)
    const slow = sandbox('slow', { conditions: [
      { type: 'Ready', status: 'False', reason: 'DependenciesNotReady' },
      { type: 'PodScheduled', status: 'False', reason: 'Unschedulable' },
    ] })
    const attention = [{
      id: 'sandbox:slow', category: 'sandbox', rule: 'stalled', severity: 'warning', issue: true,
      subject: { kind: 'Sandbox', group: 'agents.x-k8s.io', name: 'slow', namespace: NS },
      title: 'Pod can’t be scheduled', nextStep: 'Check the scheduler',
    }] as const
    const data = liveData({ sandboxes: [slow] })
    const server = serverReadings(liveWorkspace({ attention: [...attention] }))
    expect(page(<SandboxesScreen {...screenProps(data)} server={server} />)).toMatch(/class="[^"]*amber[^"]*"[^>]*>Can’t schedule</)
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

  it('says an empty list is unknown and names the missing permission when denied', () => {
    expect(page(<SandboxesScreen {...screenProps(liveData({ sandboxes: DENIED }))} />)).toContain('Unknown: no access to Sandboxes (missing list sandboxes.agents.x-k8s.io).')
    expect(page(<ClaimsScreen {...screenProps(emptyData())} />)).toContain('No SandboxClaims found in the requested scope.')
    expect(page(<WarmPoolsScreen {...screenProps(emptyData({ warmPools: read([], PARTIAL) }))} />)).toContain('No SandboxWarmPools found in the namespaces Radar could read.')
  })
})

describe('Agent Sandbox list queries', () => {
  it('are refreshed by what refreshes resource lists: writes, and live events for their kind', () => {
    for (const kind of [SANDBOX_KIND, SANDBOX_CLAIM_KIND, SANDBOX_WARM_POOL_KIND]) {
      const client = new QueryClient()
      const key = agentSandboxListKey(kind, NS)
      const stale = () => client.getQueryState(key)?.isInvalidated
      client.setQueryData(key, { items: [], coverage: { status: 'unknown' } })
      // A live event invalidates ['resources', plural], the plural the event's kind and group map to.
      void client.invalidateQueries({ queryKey: ['resources', kindToPluralWithGroup(kind.kind, kind.group)] })
      expect(stale()).toBe(true)
      client.setQueryData(key, { items: [], coverage: { status: 'unknown' } })
      void client.invalidateQueries({ queryKey: ['resources'] })
      expect(stale()).toBe(true)
      // Another kind's event leaves it alone.
      client.setQueryData(key, { items: [], coverage: { status: 'unknown' } })
      void client.invalidateQueries({ queryKey: ['resources', 'pods'] })
      expect(stale()).toBe(false)
      // The standard list's key for the same kind (group in the third place) is a different entry.
      expect(client.getQueryData(['resources', kind.name, kind.group, [NS]])).toBeUndefined()
    }
  })
})
