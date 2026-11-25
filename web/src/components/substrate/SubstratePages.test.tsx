// @vitest-environment jsdom

import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import type { SubstrateActorDetail, SubstrateActorLogs, SubstrateRef, SubstrateTemplate } from '../../api/substrateWorkspace'
import { ApiError } from '../../api/client'
import { decodeDrawerTrail } from '../../utils/drawer-trail'
import { actorDetail, counts, workspace } from './substrate.fixtures'

type Read<T> = { data?: T; error: unknown; isLoading: boolean; isRefetchError: boolean; dataUpdatedAt: number; isFetching?: boolean; refetch?: () => void }

const state = vi.hoisted(() => ({
  context: 'cluster-B',
  template: undefined as unknown as Read<SubstrateTemplate>,
  actor: undefined as unknown as Read<SubstrateActorDetail>,
  logs: undefined as SubstrateActorLogs | undefined,
  reads: [] as string[],
  workspace: undefined as unknown,
}))

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: state.context } }) }))
vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useContexts: () => ({ data: [{ name: 'cluster-A' }, { name: 'cluster-B' }] }),
  usePrometheusStatus: () => ({ data: { connected: false } }),
}))
vi.mock('../useContextSwitchFlow', () => ({ useContextSwitchFlow: () => ({ requestSwitch: vi.fn(), confirmDialog: null }) }))
vi.mock('../workload/WorkloadView', () => ({ WorkloadView: () => <div>WorkerPool controls for the active cluster</div> }))
vi.mock('../../api/substrateWorkspace', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/substrateWorkspace')>()),
  useSubstrateTemplate: (ref: SubstrateRef | null) => {
    if (ref) state.reads.push(`template ${ref.name}`)
    return ref ? state.template : { data: undefined, error: null, isLoading: false, isRefetchError: false, dataUpdatedAt: 0 }
  },
  useSubstrateActor: (ref: SubstrateRef | null) => {
    if (ref) state.reads.push(`actor ${ref.name}`)
    return ref ? state.actor : { data: undefined, error: null, isLoading: false, isRefetchError: false, dataUpdatedAt: 0 }
  },
  useSubstrateActorLogs: () => ({ data: state.logs, error: null, isLoading: !state.logs }),
  useSubstrateWorkspace: () => ({ data: state.workspace }),
  useSubstratePool: () => ({ data: undefined, error: null, isLoading: true }),
}))

const { SubstrateTemplatePage } = await import('./SubstrateTemplates')
const { SubstratePoolPage } = await import('./SubstratePoolPage')
const { SubstrateActorPage } = await import('./SubstrateActorPage')
const { actorDrawerParams, resourceDrawerParams } = await import('./SubstrateView')

const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/<\/?span[^>]*>/g, '')
const render = (node: React.ReactNode, route: string) =>
  text(renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={[route]}>{node}</MemoryRouter></QueryClientProvider>))

const ready = workspace().pools[0]
function template(name: string): SubstrateTemplate {
  return {
    apiTier: workspace().apiTier,
    template: { atespace: 'team', name, golden: { state: 'ready' }, matchingPools: [{ namespace: ready.namespace, name: ready.name }], matchingPoolsComplete: true, preferredFidelity: 'memory' },
    coverage: { templates: { state: 'full' }, actors: { state: 'full' }, workers: { state: 'full' }, pools: { state: 'full' } },
  } as unknown as SubstrateTemplate
}
const ok = <T,>(data: T): Read<T> => ({ data, error: null, isLoading: false, isRefetchError: false, dataUpdatedAt: Date.now() - 120_000 })
const failed = <T,>(data: T, error: unknown): Read<T> => ({ data, error, isLoading: false, isRefetchError: true, dataUpdatedAt: Date.now() - 120_000 })
const apiError = (status: number, code: string) => new ApiError(`${code} from Substrate`, status, { error: `${code} from Substrate`, error_code: code })

describe('a detail page pinned to another context', () => {
  it('shows the WorkerPool page’s mismatch screen instead of mounting the active cluster’s controls', () => {
    const html = render(<SubstratePoolPage pool={{ namespace: 'agents', name: 'pool' }} onOpenResource={() => {}} />, '/substrate/workerpools/agents/pool?ctx=cluster-A')
    expect(html).toContain('pool is not in cluster-B')
    expect(html).toContain('Switch back to cluster-A')
    expect(html).not.toContain('WorkerPool controls for the active cluster')
  })

  it('shows the template page’s mismatch screen and reads nothing', () => {
    state.reads = []
    state.template = ok(template('template-from-B'))
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'template' }} onOpenSubject={() => {}} />, '/substrate/templates/team/template?ctx=cluster-A')
    expect(html).toContain('template is not in cluster-B')
    expect(html).not.toContain('template-from-B')
    expect(state.reads).toEqual([])
  })

  it('reads nothing for an actor page pinned to another context', () => {
    state.reads = []
    state.actor = ok(actorDetail())
    const html = render(<SubstrateActorPage actor={{ atespace: 'research', name: 'lit-review-0193' }} onOpenSubject={() => {}} />, '/substrate/actors/research/lit-review-0193?ctx=cluster-A')
    expect(html).toContain('lit-review-0193 is not in cluster-B')
    expect(state.reads).toEqual([])
  })

  it('opens the page when the pin matches', () => {
    state.template = ok(template('template'))
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'template' }} onOpenSubject={() => {}} />, '/substrate/templates/team/template?ctx=cluster-B')
    expect(html).toContain('At a glance')
  })
})

describe('a failed template', () => {
  it('says what a failed build means once, in the banner, not again in At a glance', () => {
    const t = template('broken')
    ;(t.template as any).golden = { state: 'failed', error: 'killed for exceeding its memory limit' }
    state.template = ok(t)
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'broken' }} onOpenSubject={() => {}} />, '/substrate/templates/team/broken?ctx=cluster-B')
    expect(html).toContain('Startup snapshot failed')
    expect(html).toContain('New actors without a seed tag start from a cold boot.')
    expect(html).toContain('Failed · cold startup')
    expect(html.split('does not retry').length - 1).toBe(1)
  })

  it('takes the cause-specific remedy from Substrate’s own row for it, with the reason code stripped', () => {
    const t = template('broken')
    ;(t.template as any).golden = { state: 'failed', error: 'GoldenActorCrashed: golden actor crashed: killed for exceeding its memory limit' }
    state.template = ok(t)
    const golden = { id: 'g', rule: 'goldenSnapshotFailed', severity: 'warning', title: 't', reading: 'Its golden actor was killed for exceeding its memory limit before the snapshot was taken. Substrate does not retry a failed build. Substrate’s message: golden actor crashed: killed for exceeding its memory limit.', nextStep: 'Recreate the template with a higher memory limit.', command: 'kubectl ate get actor-template broken -a team -o yaml', subject: { kind: 'ActorTemplate', atespace: 'team', name: 'broken' }, count: 1 }
    state.workspace = { ...workspace(), needsAttention: [golden] }
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'broken' }} onOpenSubject={() => {}} />, '/substrate/templates/team/broken?ctx=cluster-B')
    expect(html).toContain('Next step: Recreate the template with a higher memory limit.')
    expect(html).toContain('golden actor crashed: killed for exceeding its memory limit')
    expect(html).not.toContain('GoldenActorCrashed:')
    // The cause leads once; Substrate's own words and the no-retry note fold under Details, after the step.
    expect(html.split('killed for exceeding its memory limit before').length - 1).toBe(1)
    expect(html.indexOf('Its golden actor was killed')).toBeLessThan(html.indexOf('Next step:'))
    expect(html.indexOf('Details')).toBeGreaterThan(html.indexOf('Next step: Recreate'))
    expect(html.indexOf('golden actor crashed: killed')).toBeGreaterThan(html.indexOf('Details'))
    state.workspace = undefined
  })
})

describe('a crashed golden-build actor', () => {
  it('gives its template’s step, with no revert or move', () => {
    state.context = 'cluster-B'
    const golden = { id: 'goldenSnapshotFailed:research/langgraph-research', rule: 'goldenSnapshotFailed', severity: 'warning', title: 't', reading: 'r', nextStep: 'Recreate the template with a higher memory limit; templates can’t be edited.', subject: { kind: 'ActorTemplate', atespace: 'research', name: 'langgraph-research' }, count: 1 }
    state.actor = ok(actorDetail({ attention: [golden] } as Partial<SubstrateActorDetail>))
    const html = render(<SubstrateActorPage actor={{ atespace: 'research', name: 'lit-review-0193' }} onOpenSubject={() => {}} />, '/substrate/actors/research/lit-review-0193?ctx=cluster-B')
    expect(html).toContain('This actor built research / langgraph-research’s golden snapshot')
    expect(html).toContain('Recreate the template with a higher memory limit')
    expect(html).not.toContain('kubectl ate revert')
    expect(html).not.toContain('kubectl ate update')
    expect(html.split('Recreate the template with a higher memory limit').length - 1).toBe(1)
  })
})

describe('a template whose actors crashed', () => {
  it('says how many of its actors crashed and why, beside the memory it sets', () => {
    const t = template('langgraph-research')
    t.template.memoryBytes = 2 * 1024 ** 3
    t.template.cpuMillicores = 2000
    t.template.attentionCount = 11
    t.template.actors = counts({ crashed: 11, running: 30 })
    state.template = ok(t)
    const crash = { id: 'c', rule: 'crashedActors', severity: 'critical', title: 't', reading: 'r', nextStep: 'Fix it.', subject: { kind: 'Actors', filter: { state: 'crashed' } }, count: 15,
      groups: [{ subject: { kind: 'ActorTemplate', atespace: 'team', name: 'langgraph-research' }, count: 11, cause: 'memoryLimit', nextStep: 'Create a template with a higher memory limit.', command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' }] }
    state.workspace = { ...workspace(), needsAttention: [crash] }
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'langgraph-research' }} onOpenSubject={() => {}} />, '/substrate/templates/team/langgraph-research?ctx=cluster-B')
    expect(html).toContain('11 reported crashed →')
    expect(html).toContain('Killed over the 2Gi memory limit per actor')
    expect(html).toContain('Create a template with a higher memory limit.')
    expect(html).toContain('Golden snapshot ready')
    expect(html).toContain('2 CPU · 2Gi')
    expect(html).toMatch(/Crash recovery steps.*?inert=.*?Create a template.*?Copy command/)
    expect(html.match(/aria-label="Copy command"/g)?.length).toBe(1)
    state.workspace = undefined
  })
})

describe('a template whose golden actor and actors were both killed over its memory limit', () => {
  const move = 'kubectl ate update actor <name> -a <atespace> --template <new-template>'
  const revert = 'kubectl ate revert actor <name> -a <atespace>'
  const crashRow = (count: number) => ({ id: 'c', rule: 'crashedActors', severity: 'critical', title: 't', reading: 'r', nextStep: 'Fix it.', subject: { kind: 'Actors', filter: { state: 'crashed' } }, count,
    groups: [{ subject: { kind: 'ActorTemplate', atespace: 'sandbox-dev', name: 'mcp-browser' }, count, cause: 'memoryLimit', nextStep: 'Move the actors to a template with more memory, then revert them. A snapshot taken under the old template may restore only its volumes.', command: move, thenCommand: revert }] })
  const goldenRow = { id: 'g', rule: 'goldenSnapshotFailed', severity: 'warning', title: 't', reading: 'Its golden actor was killed for exceeding its memory limit before the snapshot was taken. Substrate does not retry a failed build.', nextStep: 'Recreate the template with a higher memory limit; templates can’t be edited.', subject: { kind: 'ActorTemplate', atespace: 'sandbox-dev', name: 'mcp-browser' }, count: 1 }
  const page = (cause: 'memoryLimit' | undefined, count: number) => {
    const t = template('mcp-browser')
    ;(t.template as any).atespace = 'sandbox-dev'
    ;(t.template as any).golden = { state: 'failed', error: 'GoldenActorCrashed: golden actor crashed: killed for exceeding its memory limit', cause }
    state.template = ok(t)
    state.workspace = { ...workspace(), needsAttention: [crashRow(count), goldenRow] }
    try {
      return render(<SubstrateTemplatePage template={{ atespace: 'sandbox-dev', name: 'mcp-browser' }} onOpenSubject={() => {}} />, '/substrate/templates/sandbox-dev/mcp-browser?ctx=cluster-B')
    } finally {
      state.workspace = undefined
    }
  }
  const words = (html: string) => html.replace(/<[^>]+>/g, ' ').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/\s+/g, ' ')

  it('uses one startup callout for the joint recovery plan and folds the commands in order', () => {
    const html = page('memoryLimit', 1)
    const w = words(html)
    expect(w.indexOf('Startup snapshot failed')).toBeLessThan(w.indexOf('At a glance'))
    expect(html).not.toContain('bg-red-50')
    const plan = 'Recreate the template with a higher memory limit, move the crashed actor to it, then revert it. The move may keep only its snapshot’s volumes.'
    expect(w).toContain(`Next step: ${plan}`)
    expect(w.split('Next step:').length - 1).toBe(1)
    expect(w).not.toContain('Recreate the template with a higher memory limit; templates can’t be edited.')
    expect(w.indexOf(plan)).toBeLessThan(w.indexOf(move))
    expect(w.indexOf(move)).toBeLessThan(w.indexOf(revert))
    expect(html).toMatch(/Crash recovery steps.*?inert=.*?Copy command/)
  })

  it('keeps the actor remedy separately folded when the startup build failed for another reason', () => {
    const w = words(page(undefined, 3))
    expect(w).toContain('Move the actors to a template with more memory, then revert them.')
    expect(w).toContain('Next step: Recreate the template with a higher memory limit; templates can’t be edited.')
    expect(w.indexOf('Startup snapshot failed')).toBeLessThan(w.indexOf('Crash recovery steps'))
  })

  it('offers no command, and keeps the server’s step, when kagent runs some of its crashed actors', () => {
    const t = template('mcp-browser')
    ;(t.template as any).atespace = 'sandbox-dev'
    ;(t.template as any).golden = { state: 'failed', error: 'GoldenActorCrashed: golden actor crashed: killed for exceeding its memory limit', cause: 'memoryLimit' }
    state.template = ok(t)
    const step = 'Move the actors to a template with more memory, then revert them. A snapshot taken under the old template may restore only its volumes. Recover the kagent sessions among them through kagent instead (RevertSession, or delete the chat).'
    const row = crashRow(2)
    row.groups = [{ subject: { kind: 'ActorTemplate', atespace: 'sandbox-dev', name: 'mcp-browser' }, count: 2, cause: 'memoryLimit', nextStep: step, kagent: true } as any]
    state.workspace = { ...workspace(), needsAttention: [row, goldenRow] } as any
    try {
      const w = words(render(<SubstrateTemplatePage template={{ atespace: 'sandbox-dev', name: 'mcp-browser' }} onOpenSubject={() => {}} />, '/substrate/templates/sandbox-dev/mcp-browser?ctx=cluster-B'))
      expect(w).toContain(step)
      expect(w).not.toContain(move)
      expect(w).not.toContain(revert)
      expect(w).not.toContain('move the crashed actors to it, then revert them')
    } finally {
      state.workspace = undefined
    }
  })

  it('defines the startup snapshot in At a glance', () => {
    const t = template('mcp-browser')
    ;(t.template as any).golden = { state: 'failed', error: 'x' }
    state.template = ok(t)
    const raw = renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/substrate/templates/team/mcp-browser?ctx=cluster-B']}><SubstrateTemplatePage template={{ atespace: 'team', name: 'mcp-browser' }} onOpenSubject={() => {}} /></MemoryRouter></QueryClientProvider>)
    expect(raw).toMatch(/decoration-dotted[^>]*>Startup snapshot<\/span>/)
  })
})

describe('a template’s banners', () => {
  it('uses the strongest template finding for the callout and header', () => {
    const t = template('stranded')
    ;(t.template as any).matchingPools = []
    ;(t.template as any).golden = { state: 'failed', error: 'x' }
    state.template = ok(t)
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'stranded' }} onOpenSubject={() => {}} />, '/substrate/templates/team/stranded?ctx=cluster-B')
    const banner = (title: string) => html.slice(html.lastIndexOf('<div class="p-3 border rounded-lg', html.indexOf(title)), html.indexOf(title))
    expect(banner('No worker pool matches')).toContain('status-alert')
    expect(html).not.toContain('bg-amber-50')
    expect(html).toContain('Failed · cold startup')
    expect(html).not.toContain('bg-red-50')
  })
})

describe('a detail read whose refresh failed', () => {
  it('keeps the template with a stale notice saying how old it is when the refresh failed for a passing reason', () => {
    state.template = failed(template('template'), apiError(502, 'unreachable'))
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'template' }} onOpenSubject={() => {}} />, '/substrate/templates/team/template?ctx=cluster-B')
    expect(html).toContain('At a glance')
    expect(html).toContain('Last refresh failed: unreachable from Substrate')
    expect(html).toContain('showing data from 2m ago')
  })

  it('drops the kept template when Substrate says it is gone or no longer readable', () => {
    state.template = failed(template('template'), apiError(404, 'notFound'))
    let html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'template' }} onOpenSubject={() => {}} />, '/substrate/templates/team/template?ctx=cluster-B')
    expect(html).toContain('Substrate has no template template')
    expect(html).not.toContain('At a glance')
    state.template = failed(template('template'), apiError(403, 'denied'))
    html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'template' }} onOpenSubject={() => {}} />, '/substrate/templates/team/template?ctx=cluster-B')
    expect(html).toContain('Substrate denied reading template')
  })

  it('drops the kept actor on a definitive failure and marks it stale otherwise', () => {
    state.actor = failed(actorDetail(), apiError(404, 'notFound'))
    let html = render(<SubstrateActorPage actor={{ atespace: 'research', name: 'lit-review-0193' }} onOpenSubject={() => {}} />, '/substrate/actors/research/lit-review-0193?ctx=cluster-B')
    expect(html).toContain('Substrate has no actor lit-review-0193')
    expect(html).not.toContain('At a glance')
    state.actor = failed(actorDetail(), apiError(502, 'unreachable'))
    html = render(<SubstrateActorPage actor={{ atespace: 'research', name: 'lit-review-0193' }} onOpenSubject={() => {}} />, '/substrate/actors/research/lit-review-0193?ctx=cluster-B')
    expect(html).toContain('At a glance')
    expect(html).toContain('Last refresh failed: unreachable from Substrate')
  })
})

describe('an actor’s snapshots', () => {
  const page = (snapshots: NonNullable<SubstrateActorDetail['actor']['snapshots']>) => {
    const detail = actorDetail()
    state.actor = ok(actorDetail({ actor: { ...detail.actor, snapshots }, row: { ...detail.row, resumeGeneration: snapshots.find((s) => s.durable === 'completed')?.generation } }))
    return render(<SubstrateActorPage actor={{ atespace: 'research', name: 'lit-review-0193' }} onOpenSubject={() => {}} />, '/substrate/actors/research/lit-review-0193?tab=snapshots&ctx=cluster-B')
  }

  it('says where each copy is kept, durable or only on its node, never "stored" twice', () => {
    const html = page([
      { generation: 6, fidelity: 'memory', owner: 'actor', durable: 'pending', locality: 'ip-10-0-17-21.ec2.internal' },
      { generation: 5, fidelity: 'memory', owner: 'actor', durable: 'completed' },
      { generation: 4, fidelity: 'rootfs', owner: 'actor', durable: 'none', locality: 'ip-10-0-17-21.ec2.internal' },
    ])
    expect(html).toContain('Where it is kept')
    expect(html.indexOf('Resume source')).toBeLessThan(html.indexOf('Where it is kept'))
    expect(html).toContain('Generation 5 · memory · Durable copy')
    expect(html).not.toContain('>Stored<')
    expect(html).toContain('Only on its nodeUpload pending. Recorded locality:')
    expect(html).toContain('Durable copyUploaded to object storage.')
    expect(html).toContain('No durable copy recorded. Recorded locality:')
    // The hostname stays one unbroken token.
    const raw = renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/substrate/actors/research/lit-review-0193?tab=snapshots&ctx=cluster-B']}><SubstrateActorPage actor={{ atespace: 'research', name: 'lit-review-0193' }} onOpenSubject={() => {}} /></MemoryRouter></QueryClientProvider>)
    expect(raw).toMatch(/<button[^>]*>.*?class="inline-block whitespace-nowrap">ip-10-0-17-21\.ec2\.internal<\/span><\/button>/)
    // Substrate records no upload time, so none is invented.
    expect(html).not.toMatch(/uploaded \d|upload(ed)? at/i)
  })

  it('says why earlier generations are absent only from what Substrate’s record holds, and nothing when none are', () => {
    const missing = page([{ generation: 6, fidelity: 'memory', owner: 'actor', durable: 'completed' }])
    expect(missing).toContain('Substrate’s record of an actor lists only its active and in-progress generations, so 5 earlier generations are not listed.')
    expect(missing).not.toContain('pruned')
    const whole = page([{ generation: 2, fidelity: 'memory', owner: 'actor', durable: 'completed' }, { generation: 1, fidelity: 'memory', owner: 'actor', durable: 'completed' }])
    expect(whole).not.toContain('earlier generation')
  })
})

describe('actor log availability', () => {
  function logs(reasonCode: SubstrateActorLogs['reasonCode']) {
    const detail = actorDetail()
    state.actor = ok(detail)
    state.logs = {
      actor: detail.actor, state: 'crashed', origin: 'lastNode', reasonCode, node: 'former-node',
      source: 'all', lines: [], searched: [], scanned: 0, windowFull: false,
    }
    try {
      return render(<SubstrateActorPage actor={detail.actor} onOpenSubject={() => {}} />, '/substrate/actors/research/lit-review-0193?tab=logs&ctx=cluster-B')
    } finally {
      state.logs = undefined
    }
  }

  it('gives one unavailable card and an external query when the recorded node is gone', () => {
    const html = logs('nodeGone')
    expect(html.match(/Worker logs unavailable/g)).toHaveLength(1)
    expect(html).toContain('former-node, is gone from the cluster')
    expect(html).toContain('ate.actor.name="lit-review-0193" ate.atespace="research"')
    expect(html).not.toContain('Log source')
    expect(html).not.toContain('No matching log lines')
  })

  it('keeps source controls when the log was read and returned no matches', () => {
    const html = logs('noMatch')
    expect(html).toContain('No matching log lines')
    expect(html).toContain('Container output')
    expect(html).not.toContain('Worker logs unavailable')
  })
})

describe('one drawer at a time', () => {
  it('opens a resource from the actor drawer in one update that drops the actor', () => {
    const before = new URLSearchParams('actor=team%2Falice&tab=x')
    const after = resourceDrawerParams(before, { kind: 'Pod', namespace: 'agents', name: 'worker-0' })
    expect(after.has('actor')).toBe(false)
    expect(decodeDrawerTrail(after.get('drawer'))).toMatchObject([{ kind: 'Pod', namespace: 'agents', name: 'worker-0' }])
    expect(after.get('tab')).toBe('x')
  })

  it('opens an actor in one update that drops the resource drawer', () => {
    const after = actorDrawerParams(new URLSearchParams('drawer=Pod%3Aagents%3Aworker-0'), { atespace: 'team', name: 'alice' })
    expect(after.has('drawer')).toBe(false)
    expect(after.get('actor')).toBe('team/alice')
  })
})

describe('the screen gate on a Radar without the Substrate workspace', () => {
  it('shows the upgrade note, not a generic failure', async () => {
    const { SubstrateScreenGate } = await import('./shared')
    const { RadarFeatureUnsupportedError } = await import('../../api/radarFeatures')
    const error = new RadarFeatureUnsupportedError('substrateWorkspace', { currentVersion: '1.0.0' })
    const query = { data: undefined, error, isLoading: false, isFetching: false, refetch: vi.fn(), isRefetchError: false, dataUpdatedAt: 0 }
    const html = render(<SubstrateScreenGate query={query} discoveryPending={false} discovered>{() => <div>Workspace</div>}</SubstrateScreenGate>, '/substrate')
    expect(html).toContain('Agent Substrate needs a newer Radar on this cluster')
    expect(html).not.toContain('Agent Substrate unavailable')
  })
})

describe('An in-cluster Radar or Radar Hub whose operator turned the API off', () => {
  it('says so on Templates and Workers, not an error', async () => {
    const { SubstrateTemplatesScreen } = await import('./SubstrateTemplates')
    const { SubstrateWorkersScreen } = await import('./SubstrateWorkers')
    const { notConnectedWorkspace, apiDisabledTier, status: statusOf } = await import('./substrate.fixtures')
    const ws = notConnectedWorkspace(apiDisabledTier)
    const st = statusOf({ apiTier: apiDisabledTier, canConfigure: false })
    for (const html of [
      render(<SubstrateTemplatesScreen workspace={ws} status={st} />, '/substrate/templates'),
      render(<SubstrateWorkersScreen workspace={ws} status={st} onOpenSubject={() => {}} />, '/substrate/workers'),
    ]) {
      expect(html).toContain('Turned off for this Radar')
      expect(html).toContain('>Connection settings</button>')
      expect(html).not.toContain('couldn’t read')
    }
  })

  it('reads a template page refused because the API is off as that, with what to ask for', () => {
    state.template = { data: undefined, error: apiError(403, 'apiDisabled'), isLoading: false, isRefetchError: false, dataUpdatedAt: 0 }
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'x' }} onOpenSubject={() => {}} />, '/substrate/templates/team/x?ctx=cluster-B')
    expect(html).toContain('Turned off for this Radar')
    expect(html).toContain('substrate.api.enabled')
  })
})

it('uses template-local coverage while the workspace loads and keeps configuration on Spec', () => {
  state.context = 'cluster-B'
  state.workspace = undefined
  const t = template('template')
  t.template.actors = counts({})
  t.template.attentionCount = 0
  t.spec = { containers: [{ name: 'only-on-spec', image: 'example/image' }], volumes: [], snapshot: { preferredFidelity: 'memory', golden: 'enabled' } }
  const page = <SubstrateTemplatePage template={t.template} onOpenSubject={() => {}} />
  state.template = ok(t)
  const overview = render(page, '/substrate/templates/team/template')
  expect(overview).toContain('0 need attention')
  expect(overview).toContain('New actors start from the golden snapshot')
  expect(overview).not.toContain('only-on-spec')
  expect(render(page, '/substrate/templates/team/template?tab=spec')).toContain('only-on-spec')
  t.coverage.actors = { state: 'partial' }
  const partial = render(page, '/substrate/templates/team/template')
  expect(partial).not.toContain('≥0')
  expect(partial).not.toContain('0 actors')
  expect(partial).toContain('Unknown')
})

it('puts the actor template and resource facts in one grid and shows the matched pool verdict', () => {
  state.context = 'cluster-B'
  const ws = workspace()
  state.workspace = ws
  const detail = actorDetail({ matchingPools: [ws.pools[0]], matchingPoolsComplete: true })
  state.actor = ok(detail)
  const page = render(<SubstrateActorPage actor={detail.actor} onOpenSubject={() => {}} />, '/substrate/actors/research/lit-review-0193?tab=spec')
  const doc = new DOMParser().parseFromString(page, 'text/html')
  const fields = [...doc.querySelectorAll('dt')]
  expect(fields.find((field) => field.textContent === 'Template')?.parentElement).toBe(fields.find((field) => field.textContent === 'CPU')?.parentElement)
  expect(doc.querySelector('button[title^="agent-ns/agent-pool · Short of workers"]')?.textContent).toBe('agent-pool')
})
