import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import type { SubstrateActorDetail, SubstrateRef, SubstrateTemplate } from '../../api/substrateWorkspace'
import { ApiError } from '../../api/client'
import { decodeDrawerTrail } from '../../utils/drawer-trail'
import { actorDetail, workspace } from './substrate.fixtures'

type Read<T> = { data?: T; error: unknown; isLoading: boolean; isRefetchError: boolean; dataUpdatedAt: number; isFetching?: boolean; refetch?: () => void }

const state = vi.hoisted(() => ({
  context: 'cluster-B',
  template: undefined as unknown as Read<SubstrateTemplate>,
  actor: undefined as unknown as Read<SubstrateActorDetail>,
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
  useSubstrateActorLogs: () => ({ data: undefined, error: null, isLoading: true }),
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
    coverage: { templates: { state: 'full' }, actors: { state: 'full' }, pools: { state: 'full' } },
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
    expect(html).toContain('No golden snapshot: new actors from the template’s default boot cold')
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

describe('a template whose actors crashed', () => {
  it('says how many of its actors crashed and why, beside the memory it sets', () => {
    const t = template('langgraph-research')
    ;(t.template as any).memoryBytes = 2 * 1024 ** 3
    ;(t.template as any).cpuMillicores = 2000
    state.template = ok(t)
    const crash = { id: 'c', rule: 'crashedActors', severity: 'critical', title: 't', reading: 'r', nextStep: 'Fix it.', subject: { kind: 'Actors', filter: { state: 'crashed' } }, count: 15,
      groups: [{ subject: { kind: 'ActorTemplate', atespace: 'team', name: 'langgraph-research' }, count: 11, cause: 'memoryLimit', nextStep: 'Create a template with a higher memory limit.', command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' }] }
    state.workspace = { ...workspace(), needsAttention: [crash] }
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'langgraph-research' }} onOpenSubject={() => {}} />, '/substrate/templates/team/langgraph-research?ctx=cluster-B')
    expect(html).toContain('11 of its actors are crashed')
    expect(html).toContain('Killed over the 2Gi memory limit it sets per actor.')
    expect(html).not.toContain('they were killed for exceeding')
    expect(html).toContain('Next step: Create a template with a higher memory limit.')
    expect(html).toContain('The 11 crashed actors →')
    // Beside a crash callout, the header chip names what is ready: the golden snapshot, not the template.
    expect(html).toContain('Golden snapshot ready')
    expect(html).toContain('a ceiling, not a guarantee')
    // Compact: the cause and the way to the actors on one line, then the step with its one command.
    expect(html).toMatch(/Killed over the 2Gi memory limit it sets per actor\.(<\/span>)?<button[^>]*>The 11 crashed actors →<\/button><\/div>/)
    expect(html.indexOf('The 11 crashed actors')).toBeLessThan(html.indexOf('Next step: Create'))
    expect(html.match(/aria-label="Copy command"/g)?.length).toBe(2) // the step's, and the Spec's YAML command
    state.workspace = undefined
  })
})

describe('a template’s banners', () => {
  it('reads each in its row’s tone: no matching pool an alert, a failed golden snapshot a warning', () => {
    const t = template('stranded')
    ;(t.template as any).matchingPools = []
    ;(t.template as any).golden = { state: 'failed', error: 'x' }
    state.template = ok(t)
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'stranded' }} onOpenSubject={() => {}} />, '/substrate/templates/team/stranded?ctx=cluster-B')
    const banner = (title: string) => html.slice(html.lastIndexOf('<div class="p-3 border rounded-lg', html.indexOf(title)), html.indexOf(title))
    expect(banner('No pool can run it')).toContain('status-alert')
    expect(banner('No golden snapshot')).toContain('bg-amber-50')
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
    state.actor = ok(actorDetail({ actor: { ...actorDetail().actor, snapshots } }))
    return render(<SubstrateActorPage actor={{ atespace: 'research', name: 'lit-review-0193' }} onOpenSubject={() => {}} />, '/substrate/actors/research/lit-review-0193?tab=snapshots&ctx=cluster-B')
  }

  it('says where each copy is kept, durable or only on its node, never "stored" twice', () => {
    const html = page([
      { generation: 6, fidelity: 'memory', owner: 'actor', durable: 'pending', locality: 'ip-10-0-17-21.ec2.internal' },
      { generation: 5, fidelity: 'memory', owner: 'actor', durable: 'completed' },
      { generation: 4, fidelity: 'rootfs', owner: 'actor', durable: 'none', locality: 'ip-10-0-17-21.ec2.internal' },
    ])
    expect(html).toContain('Where it is kept')
    expect(html).not.toContain('>Stored<')
    expect(html).toContain('Only on its nodeUpload pending. Until it finishes, its state is only on')
    expect(html).toContain('Durable copyUploaded to object storage.')
    expect(html).toContain('No durable copy recorded. Its files are only on')
    // The hostname stays one unbroken token.
    const raw = renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/substrate/actors/research/lit-review-0193?tab=snapshots&ctx=cluster-B']}><SubstrateActorPage actor={{ atespace: 'research', name: 'lit-review-0193' }} onOpenSubject={() => {}} /></MemoryRouter></QueryClientProvider>)
    expect(raw).toMatch(/class="block truncate whitespace-nowrap font-mono text-\[12\.5px\]">ip-10-0-17-21\.ec2\.internal</)
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

describe('Radar Hub, a user who isn’t an org owner', () => {
  it('shows the owners-only note on Templates and Workers, not an error or a Connect form', async () => {
    const { SubstrateTemplatesScreen } = await import('./SubstrateTemplates')
    const { SubstrateWorkersScreen } = await import('./SubstrateWorkers')
    const { notConnectedWorkspace, ownersOnlyTier, status: statusOf } = await import('./substrate.fixtures')
    const ws = notConnectedWorkspace(ownersOnlyTier)
    const st = statusOf({ apiTier: ownersOnlyTier, canConfigure: false })
    for (const html of [
      render(<SubstrateTemplatesScreen workspace={ws} status={st} />, '/substrate/templates'),
      render(<SubstrateWorkersScreen workspace={ws} status={st} onOpenSubject={() => {}} />, '/substrate/workers'),
    ]) {
      expect(html).toContain('In Radar Hub, only org owners can see Substrate’s actors, templates and workers for now. Ask an org owner.')
      expect(html).not.toContain('Who Radar reads Substrate as')
      expect(html).not.toContain('couldn’t read')
    }
  })

  it('reads a template page refused for owners only as that note', () => {
    state.template = { data: undefined, error: apiError(403, 'ownersOnly'), isLoading: false, isRefetchError: false, dataUpdatedAt: 0 }
    const html = render(<SubstrateTemplatePage template={{ atespace: 'team', name: 'x' }} onOpenSubject={() => {}} />, '/substrate/templates/team/x?ctx=cluster-B')
    expect(html).toContain('Templates are for org owners')
    expect(html).toContain('only org owners can see')
  })
})
