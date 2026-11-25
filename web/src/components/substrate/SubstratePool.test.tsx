import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { SubstratePool, SubstrateWorkerRow } from '../../api/substrateWorkspace'
import { PLACEMENT_CARD_LIMIT, Placement, SubstratePoolSummary, workerSlots } from './SubstratePoolSummary'
import { actorAttentionText } from './SubstrateActorOverview'
import { actorDetail, connectedTier, minutesAgo } from './substrate.fixtures'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/<\/?span[^>]*>/g, '')

const state = vi.hoisted(() => ({ pool: undefined as SubstratePool | undefined }))
vi.mock('../../api/substrateWorkspace', async (original) => ({
  ...(await original<typeof import('../../api/substrateWorkspace')>()),
  useSubstratePool: () => ({ data: state.pool, isLoading: false }),
}))
vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  usePrometheusStatus: () => ({ data: { connected: false } }),
}))
vi.mock('../../api/substrateHealth', async (original) => ({
  ...(await original<typeof import('../../api/substrateHealth')>()),
  useSubstrateHealth: () => ({ support: 'supported' }),
}))

function worker(i: number, overrides: Partial<SubstrateWorkerRow> = {}): SubstrateWorkerRow {
  return {
    name: `w-${i}`,
    namespace: 'agents',
    pool: 'agent-pool',
    pod: `agent-pool-worker-${i}`,
    node: 'ip-10-0-16-162.ec2.internal',
    state: 'active',
    capacity: { cpuMillicores: 16000, memoryBytes: 64 * 1024 ** 3, actors: 4 },
    allocated: { cpuMillicores: 4000, memoryBytes: 8 * 1024 ** 3, actors: 2 },
    registeredEpoch: 1,
    observedEpoch: 1,
    hasFreeSlot: true,
    heldForSeconds: 3600,
    podReady: true,
    actors: [
      { atespace: 'research', name: 'lit-review-0193', state: 'running', template: { atespace: 'research', name: 'lg' } },
      { atespace: 'research', name: 'lit-review-0194', state: 'resuming', template: { atespace: 'research', name: 'lg' } },
    ],
    ...overrides,
  }
}

function pool(workers: SubstrateWorkerRow[], desired = workers.length): SubstratePool {
  return {
    apiTier: connectedTier,
    pool: { namespace: 'agents', name: 'agent-pool', kubernetes: { namespace: 'agents', name: 'agent-pool', desired, ready: workers.length, health: 'healthy', healthReason: '' } },
    workers,
    templates: [{ atespace: 'research', name: 'lg' }],
    coverage: { workers: { state: 'full' }, actors: { state: 'full' }, templates: { state: 'full' }, pools: { state: 'full' } },
  }
}

const raw = (data: SubstratePool) => renderToStaticMarkup(<MemoryRouter><Placement data={data} onOpenActor={() => {}} onOpenPod={() => {}} /></MemoryRouter>)
const render = (data: SubstratePool) => text(renderToStaticMarkup(<MemoryRouter><Placement data={data} onOpenActor={() => {}} onOpenPod={() => {}} /></MemoryRouter>))

describe('WorkerPool placement', () => {
  it('draws one card per worker with a cell per actor, and counts the free slots in words, not cells', () => {
    const html = render(pool([worker(0)]))
    expect(html).toContain('agent-pool-worker-0')
    expect(html).toContain('lit-review-0193')
    // Free slots have no position, so they are a count: no cell repeats "free".
    expect(html).not.toContain('border-dashed')
    expect(html).not.toMatch(/>free</)
    expect(html).toContain('2 of 4 actor slots used · 2 free')
    // What the figures are, from Substrate's fields: what its actors request of the capacity it reports, not usage.
    expect(html).toContain('4 of 16 CPU · 8Gi of 64Gi reserved')
  })

  it('says a worker that takes no new actors has unused slots, not free ones, and when its Pod is Ready', () => {
    const html = render(pool([worker(0, { state: 'unavailable' })]))
    expect(html).toContain('2 of 4 actor slots used · 2 unused · takes no new actors')
    expect(html).not.toContain('2 free')
    expect(html).toContain('Its Pod is Ready, so the worker, not Kubernetes, is refusing work.')
  })

  it('keeps an unknown capacity unknown, never zero free', () => {
    const html = render(pool([worker(0, { capacity: undefined })]))
    expect(html).toContain('2 actors · slot capacity unknown')
    expect(html).not.toContain('0 free')
    expect(html).not.toContain('0 slots')
  })

  it('lets each card end at its content, in row order', () => {
    expect(raw(pool([worker(0), worker(1)]))).toMatch(/class="grid items-start gap-3/)
  })

  it('hides the placement and legend when no workers have registered', () => {
    expect(render(pool([], 3))).toBe('')
  })

  it('shows observed workers only when Substrate’s inventory is partial', () => {
    const data = { ...pool([worker(0)], 3), coverage: { ...pool([]).coverage, workers: { state: 'partial' as const, code: 'capExceeded' as const } } }
    const html = render(data)
    expect(html).toContain('agent-pool-worker-0')
    expect(html).toContain('Observed workers only')
    expect(html).not.toContain('not registered')
  })

  it('becomes one row per worker with a slot bar past the card limit', () => {
    const many = Array.from({ length: PLACEMENT_CARD_LIMIT + 1 }, (_, i) => worker(i))
    const html = render(pool(many))
    expect(html).toContain('role="img" aria-label="2 of 4 slots used"')
    expect(html).not.toContain('lit-review-0193')
  })

  it('says 0 slots for a worker that reports no actor capacity', () => {
    const html = render(pool([worker(0, { capacity: { cpuMillicores: 1, memoryBytes: 1, actors: 0 }, allocated: { cpuMillicores: 0, memoryBytes: 0, actors: 0 }, actors: [] })]))
    expect(html).toContain('0 slots: the worker reports no actor capacity')
    expect(workerSlots({ capacity: undefined, allocated: undefined, actors: undefined }).total).toBeUndefined()
  })

  it('says which actor holds a slot is unknown when actors were not read', () => {
    const html = render(pool([worker(0, { actors: undefined })]))
    expect(html).toContain('Actors were not read')
  })
})

describe('WorkerPool Overview', () => {
  function overview(data: SubstratePool, ready = data.pool.kubernetes?.ready, framed = true) {
    state.pool = data
    const resource = { spec: { replicas: data.pool.kubernetes?.desired }, status: { readyReplicas: ready } }
    return text(renderToStaticMarkup(<MemoryRouter><SubstratePoolSummary namespace="agents" name="agent-pool" resource={resource} context={framed ? 'expanded' : 'drawer'} /></MemoryRouter>))
  }

  it('keeps registrations, requested workers and observed Pod readiness separate above the placement grid', () => {
    const data = pool([worker(0), worker(1, { state: 'unavailable' })], 5)
    data.pool.kubernetes!.pods = { created: 0, ready: 0, terminating: 0 }
    data.pool.placement = { workers: { total: 2, active: 1, draining: 0, unavailable: 1 }, slots: { used: 2, total: 4 }, cpuMillicores: { used: 1, total: 2 }, memoryBytes: { used: 1, total: 2 }, complete: true }
    const html = overview(data, 2)
    expect(html).toContain('5 desired by spec')
    expect(html).toContain('2 ready reported by controller')
    expect(html).toContain('Pods observed by Radar: 0 present, 0 ready')
    expect(html).toContain('Counts differ from the controller’s status')
    expect(html.replace(/<[^>]+>/g, '')).toContain('2 registered · 1 accepting actors')
    expect(html.indexOf('At a glance')).toBeLessThan(html.indexOf('agent-pool-worker-0'))
    expect(html).toContain('Prometheus is not connected')
  })

  it('renders a partial registration count as a lower bound and an unread count as unknown', () => {
    const data = pool([worker(0)], 3)
    data.coverage.workers = { state: 'partial', deniedAtespaces: ['restricted'] }
    data.pool.placement = { workers: { total: 1, active: 1, draining: 0, unavailable: 0 }, slots: { used: 2, total: 4 }, cpuMillicores: { used: 1, total: 2 }, memoryBytes: { used: 1, total: 2 }, complete: false }
    const partial = overview(data).replace(/<[^>]+>/g, '')
    expect(partial).toContain('≥1 registered · ≥1 accepting actors')
    const denied = pool([], 3)
    denied.coverage.workers = { state: 'denied', code: 'denied' }
    const unread = overview(denied, 3).replace(/<[^>]+>/g, '')
    expect(unread).toContain('RegistrationUnknown')
    expect(unread).toContain('Substrate denied permission to list workers')
    expect(unread).toContain('3 ready reported by controller')
    expect(unread).not.toContain('0 registered')
  })

  it('leads with the distinct provisioning cause and has flat drawer sections', () => {
    const data = pool([], 3)
    data.pool.kubernetes!.health = 'unhealthy'
    data.pool.kubernetes!.healthReason = 'SandboxConfigNotFound'
    data.attention = { id: 'broken', rule: 'poolShort', severity: 'critical', title: 'Missing config', reading: 'SandboxConfig gvisor-v2 is missing.', nextStep: 'Restore the SandboxConfig.', subject: { kind: 'WorkerPool', namespace: 'agents', name: 'agent-pool' }, count: 3 }
    const html = overview(data, 0, false)
    expect(html).toContain('Sandbox configuration missing')
    expect(html).toContain('SandboxConfig gvisor-v2 is missing.')
    expect(html).toContain('Restore the SandboxConfig.')
    expect(html).toContain('Open pool')
    expect(html).not.toContain('aria-label="Legend"')
    expect(html).not.toContain('shadow-theme-sm')
  })
})

describe('Needs attention on an actor’s page', () => {
  it('says a stuck transition about this actor, not the longest one', () => {
    const d = actorDetail({ actor: { ...actorDetail().actor, state: 'resuming', crash: undefined, updateTime: minutesAgo(54) } })
    const t = actorAttentionText({ id: 's', rule: 'stuckTransition', severity: 'warning', title: '5 actors have been resuming for more than 5m', reading: 'The longest, code-review / pr-8818-reviewer, …', nextStep: 'x', subject: { kind: 'Actors' }, count: 5 }, d)
    expect(t.title).toBe('Resuming for at least 54m, longer than a transition should take')
    expect(t.message).toBe('4 other actors are in the same state.')
  })
})

describe('Workers by pool', () => {
  it('sorts the worst state first, then full workers, then by name', async () => {
    const { sortWorkers } = await import('./SubstrateWorkers')
    const order = sortWorkers([
      worker(3),
      worker(1, { state: 'unavailable' }),
      worker(2, { hasFreeSlot: false }),
      worker(0, { state: 'draining' }),
    ]).map((w) => w.pod)
    expect(order).toEqual(['agent-pool-worker-1', 'agent-pool-worker-0', 'agent-pool-worker-2', 'agent-pool-worker-3'])
  })

  it('groups under a pool header with its slots and reading, and marks a full worker', async () => {
    const { WorkersTable } = await import('./SubstrateWorkers')
    const pools = [{ namespace: 'agents', name: 'agent-pool', placement: { workers: { total: 2, active: 2, draining: 0, unavailable: 0 }, slots: { used: 6, total: 8 }, cpuMillicores: { used: 1, total: 2 }, memoryBytes: { used: 1, total: 2 }, complete: true }, capacity: 'free' as const }]
    const html = text(renderToStaticMarkup(
      <MemoryRouter>
        <WorkersTable data={{ workers: [worker(0, { hasFreeSlot: false }), worker(1)], coverage: { workers: { state: 'full' } } }} pools={pools} onOpenSubject={() => {}} grouped />
      </MemoryRouter>,
    ))
    expect(html).toContain('6 of 8 actor slots used on active workers')
    // Registered, active and desired are each named, from the pool's totals.
    expect(html).toContain('2 workers registered, 2 active')
    // Narrow (as rendered here), CPU and memory sit under the slots, still labelled.
    expect(html).toContain('4 of 16 CPU · 8Gi of 64Gi reserved')
    expect(html).toContain('Free actor slots')
    expect(html).toContain('of 4Full')
    // Slots are utilization: a full worker in a pool with room, and the pool's free slots, are neutral facts.
    const { Badge } = await import('@skyhook-io/k8s-ui')
    const getSeverityColorClass = (severity: 'neutral' | 'success') => /class="([^"]*)"/.exec(renderToStaticMarkup(<Badge severity={severity} size="sm">x</Badge>))![1]
    const rawHtml = renderToStaticMarkup(
      <MemoryRouter>
        <WorkersTable data={{ workers: [worker(0, { hasFreeSlot: false }), worker(1)], coverage: { workers: { state: 'full' } } }} pools={pools} onOpenSubject={() => {}} grouped />
      </MemoryRouter>,
    )
    const badgeClass = (label: string) => { const i = rawHtml.indexOf(`>${label}</span>`); return rawHtml.slice(rawHtml.lastIndexOf('class="', i), i) }
    expect(badgeClass('Full')).toContain(getSeverityColorClass('neutral'))
    expect(badgeClass('Free actor slots')).toContain(getSeverityColorClass('neutral'))
    expect(badgeClass('Free actor slots')).not.toContain(getSeverityColorClass('success'))
    expect(html).toContain('aria-label="Show assigned actors for agent-pool-worker-0"')
  })

  it('names registered of desired workers on the pool header, and keeps a lost count out of the state chip', async () => {
    const { WorkersTable, poolWorkersText } = await import('./SubstrateWorkers')
    const row = { placement: { workers: { total: 4, active: 2, draining: 1, unavailable: 1 }, slots: { used: 6, total: 8 }, cpuMillicores: { used: 1, total: 2 }, memoryBytes: { used: 1, total: 2 }, complete: true }, kubernetes: { namespace: 'agents', name: 'agent-pool', desired: 5, ready: 3, health: 'degraded', healthReason: '' } }
    expect(poolWorkersText(row, [])).toBe('4 of 5 desired workers registered, 2 active')
    expect(poolWorkersText({ ...row, placement: { ...row.placement, complete: false } }, [])).toBe('at least 4 of 5 desired workers registered, at least 2 active')
    const reconciling = worker(0, { workerState: { kind: 'reconciling', assigned: 13, lost: 3, reading: 'Restarted.' } })
    const html = text(renderToStaticMarkup(<MemoryRouter><WorkersTable data={{ workers: [reconciling], coverage: { workers: { state: 'full' } } }} onOpenSubject={() => {}} /></MemoryRouter>))
    expect(html).toContain('3 actors lost · Pod ready')
    // The chip is one short line; the count rides beside it, never inside it.
    expect(html).toContain('Active · reconciling')
    expect(html).not.toContain('(3 actors lost)')
    const raw = renderToStaticMarkup(<MemoryRouter><WorkersTable data={{ workers: [reconciling], coverage: { workers: { state: 'full' } } }} onOpenSubject={() => {}} /></MemoryRouter>)
    expect(raw).toMatch(/class="[^"]*whitespace-nowrap[^"]*">Active · reconciling</)
  })

  it('labels wide CPU and memory as reserved, and keeps a node’s hostname whole, never broken inside it', async () => {
    const { workerColumns } = await import('./SubstrateWorkers')
    const cols = workerColumns(() => {}, true)
    const headers = text(renderToStaticMarkup(<MemoryRouter>{cols.map((c, i) => <div key={i}>{c.header}</div>)}</MemoryRouter>))
    expect(headers).toContain('CPU reserved')
    expect(headers).toContain('Memory reserved')
    const node = cols.find((c) => c.header === 'Node')!
    const html = renderToStaticMarkup(<MemoryRouter>{node.cell(worker(0))}</MemoryRouter>)
    expect(html).toMatch(/class="block truncate whitespace-nowrap font-mono text-\[12px\]">ip-10-0-16-162\.ec2\.internal</)
    expect(html).not.toContain('overflow-wrap:anywhere')
  })

  it('marks a running actor on an unavailable worker apart from healthy running', () => {
    const impact = { kind: 'unavailable' as const, reading: 'Substrate marks its worker unavailable although the worker Pod is Ready.', podState: 'ready' as const, nextStep: 'x', command: 'y' }
    const w = worker(0, { state: 'unavailable', actors: [{ atespace: 'research', name: 'ticket-41001', state: 'running', template: { atespace: 'research', name: 'lg' }, workerImpact: impact }] })
    const html = render(pool([w]))
    expect(html).toContain('ticket-41001, Running · worker unavailable')
    expect(html).toContain('its worker is unavailable')
    expect(raw(pool([w]))).toMatch(/bg-orange-500"><\/span><span[^>]*>ticket-41001/)
  })

  it('colours a lost sandbox, an unavailable worker and a transition apart, as the legend does', () => {
    const lost = { kind: 'restarted' as const, reading: 'lost', nextStep: 'x', command: 'y' }
    const w = worker(0, { actors: [
      { atespace: 'r', name: 'lost-one', state: 'running', template: { atespace: 'r', name: 'lg' }, workerImpact: lost },
      { atespace: 'r', name: 'moving-one', state: 'resuming', template: { atespace: 'r', name: 'lg' } },
    ] })
    const html = render(pool([w]))
    expect(raw(pool([w]))).toMatch(/bg-rose-500"><\/span><span class="[^"]*">lost-one<\/span><svg[^>]*lucide-unlink/)
    expect(raw(pool([w]))).not.toContain('line-through')
    expect(raw(pool([w]))).toMatch(/bg-sky-500"><\/span><span[^>]*>moving-one/)
    expect(html).toContain('sandbox lost: its worker restarted under it')
    expect(html).toContain('resuming, suspending or reverting')
  })
})
