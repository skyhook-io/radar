import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Relationships } from '@skyhook-io/k8s-ui/types/core'
import { getSandboxStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'

const state = vi.hoisted(() => ({
  support: 'supported' as 'supported' | 'unsupported',
  workspace: undefined as AgentSandboxWorkspaceResponse | undefined,
  workspaceLoading: false,
  relationships: undefined as Relationships | undefined,
  relationshipsLoading: false,
  pod: undefined as any,
  podRequests: [] as { name: string; enabled: boolean }[],
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
}))
vi.mock('../../api/agentSandbox', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/agentSandbox')>()),
  useAgentSandboxWorkspace: () => ({ data: state.support === 'unsupported' ? undefined : state.workspace, isLoading: state.workspaceLoading }),
}))

import { AgentSandboxSummary, drawerProblem, objectReadings, otherConditions, sandboxPod, type AgentSandboxSummaryKind } from './AgentSandboxSummary'
import { NS, claim, liveClaims, liveSandboxes, warmPool } from './agentsandbox.fixtures'
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

function drawer(kind: AgentSandboxSummaryKind, resource: any) {
  const apiKind = { Sandbox: 'sandboxes', SandboxClaim: 'sandboxclaims', SandboxWarmPool: 'sandboxwarmpools' }[kind]
  return page(
    <AgentSandboxSummary kind={kind} apiKind={apiKind} namespace={NS} name={resource.metadata.name} resource={resource} onNavigate={() => {}} leadContent={ISSUES} />,
  )
}

const podChild = (name: string): Relationships => ({ children: [{ kind: 'Pod', namespace: NS, name }], ownership: { children: { complete: true } } })

beforeEach(() => {
  state.support = 'supported'
  state.workspace = liveWorkspace()
  state.workspaceLoading = false
  state.relationships = undefined
  state.relationshipsLoading = false
  state.pod = undefined
  state.podRequests = []
})

describe('Agent Sandbox drawer reading', () => {
  it('leads an image pull failure with the workspace’s reading, its next step and the Pod Radar read', () => {
    state.relationships = podChild('bad-image-pod')
    const html = drawer('Sandbox', byName('bad-image'))
    expect(html).toContain('Image can’t be pulled')
    expect(words(html)).toContain('Next step: Check the image name and the registry credentials (imagePullSecrets)')
    // The controller's message is folded under the reading, not a banner of its own.
    expect(html).toContain('Controller’s message')
    // The Pod is linked from the owner-reference children Radar read, never from the Sandbox's name.
    expect(html).toMatch(/Pod<\/dt><dd[^>]*><button[^>]*>bad-image-pod<\/button>/)
    expect(html).not.toContain('Its Pod:')
    // The reading stands in for Radar's issue cards about the same failure; they stay under Spec & status.
    expect(html).not.toContain('OPERATIONAL ISSUES')
    // One reading: no separate State line repeating the header.
    expect(html).not.toContain('>State<')
    expect(html.indexOf('Image can’t be pulled')).toBeLessThan(html.indexOf('>Pod<'))
  })

  it('leads a failed Sandbox with Pod failed and its next step', () => {
    state.relationships = podChild('failed')
    const html = drawer('Sandbox', byName('failed'))
    expect(html).toContain('Pod failed')
    expect(words(html)).toContain('Next step: Read the Pod’s logs to see why it exited')
    expect(html).toMatch(/<button[^>]*>failed<\/button>/)
  })

  it('says the Pod is unknown when Radar couldn’t read every place it could be, and never guesses it', () => {
    state.relationships = { children: [], ownership: { children: { complete: false, narrowed: true } } }
    const html = drawer('Sandbox', byName('bad-image'))
    expect(html).toContain('unknown: Radar couldn’t read every Pod that could belong to it')
    expect(html).not.toContain('>bad-image</button>')
    // A Radar that answers no completeness says nothing about absence either.
    state.relationships = { children: [] }
    expect(drawer('Sandbox', byName('bad-image'))).toContain('unknown: Radar couldn’t read every Pod')
    state.relationships = undefined
    state.relationshipsLoading = true
    expect(drawer('Sandbox', byName('bad-image'))).toContain('Reading…')
  })

  it('links the Pod a refused suspension won’t delete, and doesn’t claim the Sandbox has no Pod', () => {
    // Every Pod read and none owned by the Sandbox: the Pod blocking the suspension is foreign by definition.
    state.relationships = { children: [], ownership: { children: { complete: true } } }
    const html = drawer('Sandbox', byName('foreign'))
    expect(html).toContain('Suspension blocked')
    expect(words(html)).toContain('The Pod it won’t delete: foreign')
    expect(html).toMatch(/<button[^>]*>foreign<\/button>/)
    // The Pod may belong to something else: its owner is checked before it is removed.
    expect(words(html)).toContain('Next step: Check who owns Pod foreign (kubectl get pod foreign -n radar-agents-live -o jsonpath=\'{.metadata.ownerReferences}\') before removing it')
    expect(html).not.toContain('>Pod</dt>')
    expect(html).not.toContain('no Pod Radar can read names this Sandbox')
    // The same without the server's reading: the plain state, and still no owner-reference "none".
    state.support = 'unsupported'
    const local = drawer('Sandbox', byName('foreign'))
    expect(local).toContain('>Suspension blocked<')
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
    // The scheduler says every node is at its Pod limit, so the step is about Pod slots, not requests or tolerations.
    expect(words(html)).toContain('Next step: Add nodes, raise the nodes’ max-pods limit, or remove Pods you no longer need')
    // The reading leads; the facts follow it.
    expect(html.indexOf('Warm reserve below target')).toBeLessThan(html.indexOf('>State<'))
    // Its own state stays, in the lists' plain words.
    expect(html).toContain('>State<')
    expect(html).toContain('>Can’t schedule<')
    // PodScheduled is what the reading explains, so it isn't repeated below.
    expect(html).not.toContain('Also reported')
  })

  it('keeps the plain reading and Radar’s issues where the server has no row, or this Radar has no summary', () => {
    state.support = 'unsupported'
    state.relationships = podChild('bad-image')
    const html = drawer('Sandbox', byName('bad-image'))
    expect(html).toContain('OPERATIONAL ISSUES')
    expect(html).toContain('>Image can’t be pulled<')
    expect(html).toContain('ImagePullBackOff')
    expect(words(html)).not.toContain('Next step')
    // A healthy Sandbox has no row and no issues: its state reads plainly.
    state.support = 'supported'
    expect(drawer('Sandbox', byName('suspended'))).toMatch(/>State<.*?>Suspended</)
  })

  it('doesn’t show Radar’s issues and then swap them out while the reading loads', () => {
    state.workspace = undefined
    state.workspaceLoading = true
    expect(drawer('Sandbox', byName('bad-image'))).not.toContain('OPERATIONAL ISSUES')
  })

  it('carries a claim’s next step into its drawer, and says the missing pool once', () => {
    const html = drawer('SandboxClaim', liveClaims()[0])
    expect(html).toContain('Warm pool not found')
    expect(words(html)).toContain('Next step: Create SandboxWarmPool no-such-pool in radar-agents-live, or point the claim at an existing pool')
    expect(html.match(/not found/gi)).toHaveLength(2) // the title, and the controller's folded sentence
    expect(html).toMatch(/Warm pool<\/dt><dd[^>]*><span class="font-mono">no-such-pool</)
    // Not bound now, in words; never "never bound", which the status can't say.
    expect(html).toMatch(/Sandbox<\/dt><dd[^>]*>(<[^>]+>)*not bound</)
    expect(html).not.toContain('never bound')
  })

  it('gives a warm pool its reading, with the scheduler’s own message folded and the sandboxes it holds back linked', () => {
    const html = drawer('SandboxWarmPool', warmPool('warm', { readyReplicas: 0 }))
    expect(html).toContain('Warm reserve below target: 0/2 ready')
    expect(html).toContain('Scheduler’s message')
    expect(html).toContain('0/4 nodes are available: 4 Too many pods.')
    expect(html).toMatch(/Not ready:.*?warm-nd2zw.*?warm-4xmkm/)
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
    // The Pod's phase beside its link.
    expect(words(html)).toContain('bad-image-pod · Pending')
    // Only the Pod its owner references name was asked for.
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
    // A hyphenated reference stays on one line: each word is its own unbreakable box.
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
    // Two Pods name it as their owner: none is picked.
    state.podRequests = []
    state.relationships = { children: [{ kind: 'Pod', namespace: NS, name: 'p1' }, { kind: 'Pod', namespace: NS, name: 'p2' }], ownership: { children: { complete: true } } }
    html = drawer('Sandbox', tmpl)
    expect(state.podRequests.some((r) => r.enabled)).toBe(false)
    expect(html).not.toContain('Waiting:')
    // No Pod template in the spec: no image row rather than a padded one.
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
    // A claimed Sandbox may still end with its claim.
    const claimed = { ...byName('bad-image'), metadata: { ...byName('bad-image').metadata, ownerReferences: [{ kind: 'SandboxClaim', name: 'c', controller: true }] } }
    expect(drawer('Sandbox', claimed)).toMatch(/>Expiry<\/dt><dd[^>]*>.*?none of its own/)
  })

  it('says a claim names no warm pool and a pool names no template, in words', () => {
    const noPool = claim('direct')
    delete noPool.spec.warmPoolRef
    expect(drawer('SandboxClaim', noPool)).toMatch(/>Warm pool<\/dt><dd[^>]*>.*?no warm pool/)
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
    // A claim's state reported on its Sandbox's row.
    const onSandbox = liveAttention().map((a) => (a.id === `sandbox:${NS}/failed` ? { ...a, relations: [{ role: 'owner' as const, ref: { kind: 'SandboxClaim', group: 'extensions.agents.x-k8s.io', namespace: NS, name: 'c' }, presence: 'present' as const }] } : a))
    expect(objectReadings(liveWorkspace({ attention: onSandbox }), { kind: 'SandboxClaim', namespace: NS, name: 'c' }).folded.map((a) => a.id)).toEqual([`sandbox:${NS}/failed`])
    // Nothing found in a capped answer is unknown, not "nothing wrong".
    expect(objectReadings(liveWorkspace({ attentionTotal: 500 }), { kind: 'Sandbox', namespace: NS, name: 'running' }).unknown).toBe(true)
    expect(objectReadings(ws, { kind: 'Sandbox', namespace: NS, name: 'running' }).unknown).toBe(false)
  })

  it('drops the Pod the server names without reading it, and keeps every other related ref', () => {
    const rows = liveAttention()
    expect(drawerProblem(rows.find((a) => a.id === `sandbox:${NS}/bad-image`)!).related).toBeUndefined()
    // A refused suspension keeps the Pod the controller reported it won't delete, decided by the row's rule.
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
