import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Relationships, TimelineEvent } from '@skyhook-io/k8s-ui/types/core'
import { getSandboxStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'
import type { AgentSandboxLists } from './data'

const state = vi.hoisted(() => ({
  support: 'supported' as 'supported' | 'unsupported',
  workspace: undefined as AgentSandboxWorkspaceResponse | undefined,
  workspaceLoading: false,
  relationships: undefined as Relationships | undefined,
  relationshipsLoading: false,
  pod: undefined as any,
  podRequests: [] as { name: string; enabled: boolean }[],
  lists: undefined as AgentSandboxLists | undefined,
  events: [] as TimelineEvent[],
}))

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))
vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useRadarFeature: () => ({ support: state.support }),
  useResourceWithRelationships: () => ({ data: state.relationships ? { resource: {}, relationships: state.relationships } : undefined, isLoading: state.relationshipsLoading }),
  useResource: (_kind: string, _ns: string, name: string, _group?: string, opts?: { enabled?: boolean }) => {
    state.podRequests.push({ name, enabled: !!opts?.enabled })
    return { data: opts?.enabled && state.pod?.metadata?.name === name ? state.pod : undefined }
  },
  useResourceEvents: () => ({ k8sEvents: state.events, updates: [], isLoading: false, k8sError: null, updatesError: null }),
}))
vi.mock('../../api/agentSandbox', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/agentSandbox')>()),
  useAgentSandboxWorkspace: () => ({ data: state.support === 'unsupported' ? undefined : state.workspace, isLoading: state.workspaceLoading }),
}))
vi.mock('./data', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./data')>()),
  useAgentSandboxLists: () => ({
    data: state.lists ?? { sandboxes: { state: 'loading' }, claims: { state: 'loading' }, warmPools: { state: 'loading' } },
    isFetching: false,
    refetch: () => {},
    refreshQueries: [],
  }),
}))

import { AgentSandboxStatusBadge, AgentSandboxSummary, drawerProblem, objectReadings, otherConditions, podExitSummary, sandboxPod, type AgentSandboxSummaryKind } from './AgentSandboxSummary'
import { NS, claim, liveClaims, liveData, liveSandboxes, warmPool } from './agentsandbox.fixtures'
import { liveAttention, liveWorkspace } from './workspace.fixtures'

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
const words = (html: string) => html.replace(/<[^>]+>/g, '')
const byName = (name: string) => liveSandboxes().find((s) => s.metadata.name === name)
const ISSUES = <div>OPERATIONAL ISSUES (2)</div>

function drawer(kind: AgentSandboxSummaryKind, resource: any, framed = false) {
  const apiKind = { Sandbox: 'sandboxes', SandboxClaim: 'sandboxclaims', SandboxWarmPool: 'sandboxwarmpools' }[kind]
  return page(
    <AgentSandboxSummary kind={kind} apiKind={apiKind} namespace={NS} name={resource.metadata.name} resource={resource} framed={framed} onNavigate={() => {}} leadContent={ISSUES} />,
  )
}

const podChild = (name: string): Relationships => ({ children: [{ kind: 'Pod', namespace: NS, name }], ownership: { children: { complete: true } } })
const DENIED = { state: 'failed', message: 'forbidden', denied: true } as const

beforeEach(() => {
  state.support = 'supported'
  state.workspace = liveWorkspace()
  state.workspaceLoading = false
  state.relationships = undefined
  state.relationshipsLoading = false
  state.pod = undefined
  state.podRequests = []
  state.lists = liveData()
  state.events = []
})

describe('Agent Sandbox header status badge', () => {
  it('matches the Warm pools list verdict (Reserve below target) instead of Warming', () => {
    const html = page(<AgentSandboxStatusBadge data={warmPool('warm', { readyReplicas: 0 })} />)
    expect(html).toContain('>Reserve below target<')
    expect(html).not.toContain('>Warming<')
    expect(html).toMatch(/orange/)
  })

  it('falls back to Warming when no workspace reserve row exists', () => {
    state.support = 'unsupported'
    const html = page(<AgentSandboxStatusBadge data={warmPool('warm', { readyReplicas: 0 })} />)
    expect(html).toContain('>Warming<')
  })
})

describe('Agent Sandbox drawer and expanded reading', () => {
  it('uses flat sections in drawers and framed WorkspaceCards when expanded, with At a glance, About, and folded Controller conditions', () => {
    state.relationships = podChild('bad-image-pod')
    const flatHtml = drawer('Sandbox', byName('bad-image'), false)
    expect(flatHtml).toContain('At a glance')
    expect(flatHtml).toContain('About')
    expect(flatHtml).toContain('Controller conditions')
    expect(flatHtml).toContain('Open sandbox')
    expect(flatHtml).not.toContain('rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm')

    const framedHtml = drawer('Sandbox', byName('bad-image'), true)
    expect(framedHtml).toContain('rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm')
    expect(framedHtml).toContain('At a glance')
    expect(framedHtml).toContain('About')
    expect(framedHtml).toContain('Controller conditions')
    expect(framedHtml).not.toContain('Open sandbox')
  })

  it('leads an image pull failure with the workspace’s reading, keeps stable At a glance rows, and links Pod + Logs', () => {
    state.relationships = podChild('bad-image-pod')
    const html = drawer('Sandbox', byName('bad-image'))
    expect(html).toContain('Image can’t be pulled')
    expect(words(html)).toContain('Next step: Check the image name and the registry credentials (imagePullSecrets)')
    expect(html).toContain('Controller’s message')
    expect(html).toMatch(/Pod<\/dt><dd[^>]*>.*?<button[^>]*>bad-image-pod<\/button>/)
    expect(html).toContain('Logs →')
    expect(html).not.toContain('Its Pod:')
    expect(html).not.toContain('OPERATIONAL ISSUES')
    // Stable rows: State stays visible in At a glance alongside the callout.
    expect(html).toContain('>State<')
    expect(html.indexOf('Image can’t be pulled')).toBeLessThan(html.indexOf('>State<'))
  })

  it('puts the container exit code directly in the Pod failed callout and exposes Pod logs', () => {
    state.relationships = podChild('failed')
    state.pod = { metadata: { name: 'failed', namespace: NS }, status: { phase: 'Failed', containerStatuses: [{ name: 'agent', state: { terminated: { exitCode: 3, reason: 'Error' } } }] } }
    expect(podExitSummary(state.pod)).toBe('Container "agent" exited with code 3 (Error).')
    const html = drawer('Sandbox', byName('failed'))
    expect(html).toContain('Pod failed')
    expect(html).toContain('Container "agent" exited with code 3 (Error).')
    expect(html).toContain('Pod logs →')
    expect(html).toContain('Logs →')
    expect(html).toMatch(/<button[^>]*>failed<\/button>/)
  })

  it('says the Pod is unknown when Radar couldn’t read every place it could be, and never guesses it', () => {
    state.relationships = { children: [], ownership: { children: { complete: false, narrowed: true } } }
    const html = drawer('Sandbox', byName('bad-image'))
    expect(html).toContain('unknown: Radar couldn’t read every Pod that could belong to it')
    expect(html).not.toContain('>bad-image</button>')
    state.relationships = { children: [] }
    expect(drawer('Sandbox', byName('bad-image'))).toContain('unknown: Radar couldn’t read every Pod')
    state.relationships = undefined
    state.relationshipsLoading = true
    expect(drawer('Sandbox', byName('bad-image'))).toContain('Reading…')
  })

  it('shows Controller confirms suspension and separates unknown Pod visibility on a cleanly suspended Sandbox', () => {
    state.relationships = { children: [], ownership: { children: { complete: false } } }
    const html = drawer('Sandbox', byName('suspended'))
    expect(html).toContain('Controller confirms suspension')
    expect(html).toContain('Terminated (reported by controller)')
    expect(html).toContain('Pod visibility unknown — Radar couldn’t read every Pod that could belong to it')
  })

  it('shows Conflicting Pod with ownership check as a CommandChip on Suspension blocked, without parenthetical kubectl in prose', () => {
    state.relationships = { children: [], ownership: { children: { complete: true } } }
    const html = drawer('Sandbox', byName('foreign'))
    const cmd = "kubectl get pod foreign -n radar-agents-live -o jsonpath='{.metadata.ownerReferences}'"
    expect(html).toContain('Suspension blocked')
    expect(html).toContain('Conflicting Pod')
    expect(html).toMatch(/<button[^>]*>foreign<\/button>/)
    expect(html).toContain('data-command-line')
    expect(html).toContain(cmd)
    // Inline parenthetical command is stripped from the callout prose.
    expect(words(html)).not.toContain(`(${cmd})`)
    expect(html).not.toContain('no Pod Radar can read names this Sandbox')
    // Without the server's reading: still shows Conflicting Pod + CommandChip, never owner-ref "none".
    state.support = 'unsupported'
    const local = drawer('Sandbox', byName('foreign'))
    expect(local).toContain('Suspension blocked')
    expect(local).toContain('Conflicting Pod')
    expect(local).toContain('data-command-line')
    expect(local).not.toContain('no Pod Radar can read names this Sandbox')
  })

  it('says there is no Pod only when every Pod Radar can read was checked', () => {
    state.relationships = { children: [], ownership: { children: { complete: true } } }
    expect(drawer('Sandbox', byName('expired'))).toContain('none: no Pod Radar can read names this Sandbox as its owner')
  })

  it('reads a Sandbox the server folds into its warm pool’s row with that row, naming the pool', () => {
    const html = drawer('Sandbox', byName('warm-4xmkm'))
    expect(html).toContain('Warm reserve below target: 0/2 ready')
    expect(html).toContain('Scheduler: all nodes have reached their Pod limit.')
    expect(words(html)).toContain('Next step: Add nodes, raise the nodes’ max-pods limit, or remove Pods you no longer need')
    expect(html.indexOf('Warm reserve below target')).toBeLessThan(html.indexOf('>State<'))
    expect(html).toContain('Can’t schedule')
    expect(html).not.toContain('Also reported')
  })

  it('keeps the plain reading and Radar’s issues where the server has no row, or this Radar has no summary', () => {
    state.support = 'unsupported'
    state.relationships = podChild('bad-image')
    const html = drawer('Sandbox', byName('bad-image'))
    expect(html).toContain('OPERATIONAL ISSUES')
    expect(html).toContain('Image can’t be pulled')
    expect(html).toContain('ImagePullBackOff')
    expect(words(html)).not.toContain('Next step')
  })

  it('doesn’t show Radar’s issues and then swap them out while the reading loads', () => {
    state.workspace = undefined
    state.workspaceLoading = true
    expect(drawer('Sandbox', byName('bad-image'))).not.toContain('OPERATIONAL ISSUES')
  })

  it('shows requested pool with verified state, Browse warm pools link, and cleanup honesty on SandboxClaims', () => {
    const missingHtml = drawer('SandboxClaim', liveClaims()[0])
    expect(missingHtml).toContain('Warm pool not found')
    expect(missingHtml).toContain('Requested pool')
    expect(missingHtml).toContain('no-such-pool')
    expect(missingHtml).toContain('not found')
    expect(missingHtml).toContain('Browse warm pools in this namespace →')
    expect(missingHtml).toContain('Bound sandbox')
    expect(missingHtml).toContain('not bound')
    expect(missingHtml).not.toContain('never bound')

    // Expired claim shows controller-initiated cleanup without claiming cleanup finished.
    const expiredHtml = drawer('SandboxClaim', liveClaims()[1])
    expect(expiredHtml).toContain('Expired · controller initiated cleanup')
    expect(expiredHtml).toContain('cleanup outcome unknown unless observed')
    expect(expiredHtml).toContain('>warm</button>')
    expect(expiredHtml).toContain('0/2 ready')
    expect(expiredHtml).toContain('Browse warm pools in this namespace →')
  })

  it('gives a warm pool one reserve verdict, members sorted failures-first, referencing claims, and controller warning events', () => {
    state.events = [
      {
        id: 'ev-1',
        type: 'k8s_event',
        source: 'k8s_event',
        timestamp: new Date(Date.now() - 5 * 60_000).toISOString(),
        kind: 'SandboxWarmPool',
        namespace: NS,
        name: 'warm',
        eventType: 'Warning',
        reason: 'WarmPoolNotProgressing',
        message: '2/2 sandboxes are unschedulable past the 5m0s readiness grace period',
      } as TimelineEvent,
    ]
    const html = drawer('SandboxWarmPool', warmPool('warm', { readyReplicas: 0 }))
    expect(html).toContain('Warm reserve below target: 0/2 ready')
    expect(html).toContain('Reserve below target · 0/2 ready')
    expect(html).toContain('Scheduler’s message')
    expect(html).toContain('Members')
    expect(html).toContain('warm-4xmkm')
    expect(html).toContain('warm-nd2zw')
    expect(html).toContain('Show not ready →')
    expect(html).toContain('Claims referencing')
    expect(html).toContain('1 referencing · 0 currently bound · 1 expired')
    expect(html).toContain('claim-ok')
    expect(html).toContain('WarmPoolNotProgressing')
    expect(html).toContain('About')
    expect(html).toContain('Desired replicas')
  })

  it('names missing permissions on WarmPool members and claims when reads are denied', () => {
    state.lists = liveData({ sandboxes: DENIED, claims: DENIED })
    const html = drawer('SandboxWarmPool', warmPool('warm', { readyReplicas: 0 }))
    expect(html).toContain('missing list sandboxes.agents.x-k8s.io')
    expect(html).toContain('missing list sandboxclaims.extensions.agents.x-k8s.io')
  })
})

describe('Agent Sandbox drawer facts', () => {
  const withTemplate = (name: string, containers: any[], initContainers?: any[]) => {
    const s = byName(name)!
    return { ...s, spec: { ...s.spec, podTemplate: { spec: { containers, ...(initContainers ? { initContainers } : {}) } } } }
  }
  const podOf = (name: string, statuses: any[], phase = 'Pending') => ({ metadata: { name, namespace: NS }, status: { phase, containerStatuses: statuses } })

  it('shows the image the Pod template asks for, and the waiting reason and message from the Pod Radar read', () => {
    state.relationships = podChild('bad-image-pod')
    state.pod = podOf('bad-image-pod', [{ name: 'agent', state: { waiting: { reason: 'ImagePullBackOff', message: 'Back-off pulling image "ghcr.io/acme/agent:v9"' } } }])
    const html = drawer('Sandbox', withTemplate('bad-image', [{ name: 'agent', image: 'ghcr.io/acme/agent:v9' }]))
    expect(html).toMatch(/>Image<\/dt><dd[^>]*>.*?ghcr\.io\/acme\/agent:v9/)
    expect(html).toContain('Waiting: ImagePullBackOff')
    expect(html).toContain('Back-off pulling image "ghcr.io/acme/agent:v9"')
    expect(words(html)).toContain('bad-image-pod · Pending')
    expect(state.podRequests.filter((r) => r.enabled).map((r) => r.name)).toEqual(['bad-image-pod'])
  })

  const DNS_PULL =
    'Back-off pulling image "registry.invalid/does-not-exist:1": ErrImagePull: failed to pull and unpack image "registry.invalid/does-not-exist:1": failed to do request: Head "https://registry.invalid/v2/does-not-exist/manifests/1": dial tcp: lookup registry.invalid on 10.0.0.2:53: no such host'

  it('leads with the registry’s DNS where the pull error the Pod reports shows a failed lookup, and shows that error once, whole words to a line', () => {
    state.relationships = podChild('bad-image-pod')
    state.pod = podOf('bad-image-pod', [{ name: 'agent', state: { waiting: { reason: 'ImagePullBackOff', message: DNS_PULL } } }])
    const raw = renderToStaticMarkup(<MemoryRouter><AgentSandboxSummary kind="Sandbox" apiKind="sandboxes" namespace={NS} name="bad-image" resource={withTemplate('bad-image', [{ name: 'agent', image: 'registry.invalid/does-not-exist:1' }])} onNavigate={() => {}} /></MemoryRouter>)
    const html = text(raw)
    expect(words(html)).toContain('Next step: Check the registry hostname in the Pod template’s image, and that the nodes’ DNS resolves it')
    expect(words(html)).not.toContain('registry credentials')
    expect(html.split(DNS_PULL)).toHaveLength(2)
    expect(raw).toContain('<span class="inline-block max-w-full align-top">&quot;https://registry.invalid/v2/does-not-exist/manifests/1&quot;:</span>')
  })

  it('keeps the server’s step where the Pod’s pull error shows no failed lookup', () => {
    state.relationships = podChild('bad-image-pod')
    state.pod = podOf('bad-image-pod', [{ name: 'agent', state: { waiting: { reason: 'ErrImagePull', message: 'failed to authorize: failed to fetch anonymous token: 401 Unauthorized' } } }])
    const html = drawer('Sandbox', withTemplate('bad-image', [{ name: 'agent', image: 'ghcr.io/acme/agent:v9' }]))
    expect(words(html)).toContain('Next step: Check the image name and the registry credentials (imagePullSecrets)')
    expect(words(html)).not.toContain('DNS')
  })

  it('shows only the template where Radar didn’t read the Pod, and never reads a Pod it would have to guess', () => {
    const tmpl = withTemplate('bad-image', [{ name: 'agent', image: 'ghcr.io/acme/agent:v9' }, { name: 'sidecar', image: 'envoy:1.30' }], [{ name: 'init', image: 'busybox' }])
    state.relationships = { children: [], ownership: { children: { complete: false } } }
    let html = drawer('Sandbox', tmpl)
    expect(html).toContain('>Images<')
    for (const image of ['ghcr.io/acme/agent:v9', 'envoy:1.30', 'busybox']) expect(html).toContain(image)
    expect(words(html)).toContain('init (init)')
    expect(html).not.toContain('Waiting:')
    expect(state.podRequests.some((r) => r.enabled)).toBe(false)
    state.podRequests = []
    state.relationships = { children: [{ kind: 'Pod', namespace: NS, name: 'p1' }, { kind: 'Pod', namespace: NS, name: 'p2' }], ownership: { children: { complete: true } } }
    html = drawer('Sandbox', tmpl)
    expect(state.podRequests.some((r) => r.enabled)).toBe(false)
    expect(html).not.toContain('Waiting:')
    expect(drawer('Sandbox', byName('bad-image'))).not.toMatch(/>Images?<\/dt>/)
  })

  it('says what an exited container reports', () => {
    state.relationships = podChild('failed')
    state.pod = podOf('failed', [{ name: 'agent', state: { terminated: { exitCode: 137, reason: 'OOMKilled' } } }], 'Failed')
    const html = drawer('Sandbox', withTemplate('failed', [{ name: 'agent', image: 'agent:1' }]))
    expect(html).toContain('Exited with code 137 (OOMKilled)')
  })

  it('says a Sandbox nothing owns has no owner and no expiry, in words', () => {
    state.relationships = podChild('bad-image')
    const html = drawer('Sandbox', byName('bad-image'))
    expect(html).toMatch(/>Owner<\/dt><dd[^>]*>.*?none: no warm pool or claim owns it/)
    expect(html).toMatch(/>Expiry<\/dt><dd[^>]*>.*?no expiry/)
    expect(words(html)).not.toContain('–')
    const claimed = { ...byName('bad-image'), metadata: { ...byName('bad-image').metadata, ownerReferences: [{ kind: 'SandboxClaim', name: 'c', controller: true }] } }
    expect(drawer('Sandbox', claimed)).toMatch(/>Expiry<\/dt><dd[^>]*>.*?none of its own/)
  })

  it('says a claim names no warm pool and a pool names no template, in words', () => {
    const noPool = claim('direct')
    delete noPool.spec.warmPoolRef
    expect(drawer('SandboxClaim', noPool)).toMatch(/>Requested pool<\/dt><dd[^>]*>.*?no warm pool/)
    const bare = warmPool('bare')
    delete bare.spec.sandboxTemplateRef
    expect(drawer('SandboxWarmPool', bare)).toMatch(/>Template<\/dt><dd[^>]*>.*?no template/)
  })
})

describe('Agent Sandbox drawer reading, its parts', () => {
  it('finds an object’s own rows first, then the rows that fold it in', () => {
    const ws = liveWorkspace()
    expect(objectReadings(ws, { kind: 'Sandbox', namespace: NS, name: 'bad-image' }).own.map((a) => a.id)).toEqual([`sandbox:${NS}/bad-image`])
    const member = objectReadings(ws, { kind: 'Sandbox', namespace: NS, name: 'warm-nd2zw' })
    expect(member.own).toEqual([])
    expect(member.folded.map((a) => a.id)).toEqual([`warmPool:${NS}/warm`])
    const onSandbox = liveAttention().map((a) => (a.id === `sandbox:${NS}/failed` ? { ...a, relations: [{ role: 'owner' as const, ref: { kind: 'SandboxClaim', group: 'extensions.agents.x-k8s.io', namespace: NS, name: 'c' }, presence: 'present' as const }] } : a))
    expect(objectReadings(liveWorkspace({ attention: onSandbox }), { kind: 'SandboxClaim', namespace: NS, name: 'c' }).folded.map((a) => a.id)).toEqual([`sandbox:${NS}/failed`])
    expect(objectReadings(liveWorkspace({ attentionTotal: 500 }), { kind: 'Sandbox', namespace: NS, name: 'running' }).unknown).toBe(true)
    expect(objectReadings(ws, { kind: 'Sandbox', namespace: NS, name: 'running' }).unknown).toBe(false)
  })

  it('drops the Pod the server names without reading it, and keeps every other related ref', () => {
    const rows = liveAttention()
    expect(drawerProblem(rows.find((a) => a.id === `sandbox:${NS}/bad-image`)!).related).toBeUndefined()
    expect(drawerProblem(rows.find((a) => a.id === `sandbox:${NS}/foreign`)!).related).toEqual({ lead: 'The Pod it won’t delete:', refs: [{ kind: 'Pod', group: '', namespace: NS, name: 'foreign' }] })
    const relabeled = { ...rows.find((a) => a.id === `sandbox:${NS}/bad-image`)!, related: { lead: 'The Pod it won’t delete:', refs: [{ kind: 'Pod', group: '', namespace: NS, name: 'x' }] } }
    expect(drawerProblem(relabeled).related).toBeUndefined()
    expect(drawerProblem(rows.find((a) => a.id === `warmPool:${NS}/warm`)!).related?.refs.map((r) => r.name)).toEqual(['warm-nd2zw', 'warm-4xmkm'])
  })

  it('takes the Pod only from owner-reference children', () => {
    expect(sandboxPod(undefined, true)).toEqual({ state: 'loading' })
    expect(sandboxPod(undefined, false)).toEqual({ state: 'unknown' })
    expect(sandboxPod({ children: [{ kind: 'Pod', namespace: NS, name: 'p' }] }, false)).toEqual({ state: 'found', pods: [{ kind: 'Pod', namespace: NS, name: 'p' }] })
    expect(sandboxPod({ children: [{ kind: 'Pod', namespace: NS, name: 'p', group: 'example.com' }], ownership: { children: { complete: true } } }, false)).toEqual({ state: 'none' })
  })

  it('lists only conditions that report a problem the reading doesn’t explain; NotSuspended is not one', () => {
    const s = byName('bad-image')
    expect(otherConditions(s, getSandboxStatus(s), [])).toEqual([])
    const extra = { ...s, status: { conditions: [...s.status.conditions, { type: 'Degraded', status: 'True', reason: 'Quota', message: 'quota exceeded' }] } }
    expect(otherConditions(extra, getSandboxStatus(extra), []).map((c) => c.type)).toEqual(['Degraded'])
    const foreign = byName('foreign')
    expect(otherConditions(foreign, getSandboxStatus(foreign), [])).toEqual([])
    expect(otherConditions(claim('c'), getSandboxStatus(claim('c')), [])).toEqual([])
  })
})
