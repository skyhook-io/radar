import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { SubstratePool, SubstrateWorkerRow } from '../../api/substrateWorkspace'
import { PLACEMENT_CARD_LIMIT, Placement, workerSlots } from './SubstratePoolSummary'
import { actorAttentionText } from './SubstrateActorOverview'
import { actorDetail, connectedTier, minutesAgo } from './substrate.fixtures'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/<\/?span[^>]*>/g, '')

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
  it('draws one card per worker, one cell per slot naming its actor, the rest free', () => {
    const html = render(pool([worker(0)]))
    expect(html).toContain('agent-pool-worker-0')
    expect(html).toContain('lit-review-0193')
    expect(html.match(/border-dashed[^>]*>free</g)?.length).toBe(2)
    expect(html).toContain('2/4 slots · 4 of 16 CPU · 8Gi of 64Gi')
  })

  it('says a worker that takes nothing new has unused slots, not free ones, and when its Pod is Ready', () => {
    const html = render(pool([worker(0, { state: 'unavailable' })]))
    expect(html).toContain('2 unused')
    expect(html).not.toMatch(/border-dashed[^>]*>free</)
    expect(html).toContain('places nothing new')
    expect(html).toContain('Its Pod is Ready, so the worker, not Kubernetes, is refusing work.')
  })

  it('calls a gap in a complete worker list a registration shortfall, never missing Pods', () => {
    const html = render(pool([worker(0), worker(1, { podTerminating: true })], 3))
    expect(html).toContain('1 desired worker is not registered with Substrate')
    expect(html).toContain('The pool wants 3; Substrate has 2 registered. Kubernetes calls 2 of 3 worker Pods ready.')
    expect(html).not.toMatch(/no Pod/)
  })

  it('counts the worker Pods Radar holds, citing the WorkerPool’s own ready count only where it differs', () => {
    const data = pool([worker(0), worker(1), worker(2), worker(3, { podTerminating: true })], 5)
    data.pool.kubernetes = { ...data.pool.kubernetes!, ready: 3, pods: { created: 4, ready: 2, terminating: 1 } }
    const html = render(data)
    expect(html).toContain('Kubernetes has created 4 of 5 worker Pods; 2 are ready, and 1 is terminating. The WorkerPool’s status says 3 ready.')
    expect(html).not.toContain('may be pending')
    data.pool.kubernetes = { ...data.pool.kubernetes!, ready: 2 }
    expect(render(data)).not.toContain('status says')
  })

  it('reads placement as unknown when the workers were not read, keeping Kubernetes’ readiness apart', () => {
    const data = { ...pool([], 3), coverage: { ...pool([]).coverage, workers: { state: 'denied' as const, code: 'denied' as const } } }
    data.pool.kubernetes!.ready = 3
    const html = render(data)
    expect(html).toContain('Which actor holds which slot is unknown: Substrate’s workers were not read')
    expect(html).toContain('Kubernetes calls 3 of 3 worker Pods ready.')
    expect(html).not.toMatch(/desired workers? (have|has|is|are) no/)
    expect(html).not.toContain('not registered')
  })

  it('shows what a partial worker read saw and counts nothing as missing', () => {
    const data = { ...pool([worker(0)], 3), coverage: { ...pool([]).coverage, workers: { state: 'partial' as const, code: 'capExceeded' as const } } }
    const html = render(data)
    expect(html).toContain('agent-pool-worker-0')
    expect(html).toContain('Not every worker was read, so these are the ones Radar saw')
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
    expect(html).toContain('6 of 8 actor slots used')
    expect(html).toContain('Free actor slots')
    expect(html).toContain('of 4Full')
    expect(html).toContain('aria-label="Show the actors on agent-pool-worker-0"')
  })

  it('marks a running actor on an unavailable worker apart from healthy running', () => {
    const impact = { kind: 'unavailable' as const, reading: 'Substrate marks its worker unavailable although the worker Pod is Ready.', podState: 'ready' as const, nextStep: 'x', command: 'y' }
    const w = worker(0, { state: 'unavailable', actors: [{ atespace: 'research', name: 'ticket-41001', state: 'running', template: { atespace: 'research', name: 'lg' }, workerImpact: impact }] })
    const html = render(pool([w]))
    expect(html).toContain('ticket-41001, Running · worker unavailable')
    expect(html).toContain('its worker is unavailable or draining')
    expect(raw(pool([w]))).toMatch(/bg-amber-500"><\/span><span[^>]*>ticket-41001/)
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
