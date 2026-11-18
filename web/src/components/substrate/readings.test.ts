import { describe, expect, it } from 'vitest'
import {
  COUNTABLE_LIMIT,
  actorStateTone,
  bytesText,
  countText,
  coverageReason,
  cpuText,
  durableText,
  resumesWithText,
  revertCommand,
  segmentCount,
  segmentOfState,
  stateBarSegments,
  poolReadFailure,
  relativeTime,
  servingEvidence,
  splitReading,
  tierReading,
} from './readings'
import { substrateAttentionCount, substrateScreenCounts } from './attention'
import { decodeActorParam, encodeActorParam, parseSubstrateRoute, substrateActorPath, substrateActorsPath } from './routes'
import { segmentStates } from './SubstrateActors'
import { counts, notConnectedWorkspace, poolShortRow, sharedTier, workspace } from './substrate.fixtures'

describe('actor states', () => {
  it('reads suspended as the quietest, a transition in flight in one quiet tone, and a crash as a failure', () => {
    expect(actorStateTone('running')).toBe('healthy')
    expect(actorStateTone('suspended')).toBe('unknown')
    for (const s of ['resuming', 'suspending', 'reverting', 'deleting'] as const) expect(actorStateTone(s)).toBe('neutral')
    expect(actorStateTone('crashed')).toBe('unhealthy')
    expect(actorStateTone('unknown')).toBe('unknown')
  })

  it('never fills Unknown like Suspended: Unknown keeps Radar’s unknown slate, Suspended a quiet resting fill', async () => {
    const { actorStateFill } = await import('./readings')
    const { toneFillClass } = await import('@skyhook-io/k8s-ui')
    expect(actorStateFill('unknown')).toBe(toneFillClass('unknown'))
    expect(actorStateFill('suspended')).not.toBe(actorStateFill('unknown'))
    // Every state's fill is its own on the bar, the transitions in flight apart, sharing one.
    const states = ['running', 'suspended', 'resuming', 'crashed', 'unknown'] as const
    expect(new Set(states.map(actorStateFill)).size).toBe(states.length)
    for (const s of ['suspending', 'reverting', 'deleting'] as const) expect(actorStateFill(s)).toBe(actorStateFill('resuming'))
    // Drawn: the bar's segments and the legend's dots.
    const { renderToStaticMarkup } = await import('react-dom/server')
    const { createElement } = await import('react')
    const { SubstrateStateBar } = await import('./SubstrateStateBar')
    const html = renderToStaticMarkup(createElement(SubstrateStateBar, { counts: counts({ suspended: 900, unknown: 3, running: 97 }) }))
    const fillOf = (label: string) => new RegExp(`<span aria-hidden="true" class="inline-block h-1.5 w-1.5 rounded-full ([^"]+)"></span>(?:<[^>]+>)*${label} `).exec(html)?.[1]
    expect(fillOf('Suspended')).toBe(actorStateFill('suspended'))
    expect(fillOf('Unknown')).toBe(toneFillClass('unknown'))
  })

  it('keeps amber for a stuck transition, and says "stuck" in the chip’s words', async () => {
    const { actorStateReading } = await import('./readings')
    const now = Date.parse('2026-11-18T12:00:00Z')
    const ago = (m: number) => new Date(now - m * 60_000).toISOString()
    expect(actorStateReading('resuming', { updateTime: ago(1), now })).toEqual({ label: 'Resuming', severity: 'info' })
    expect(actorStateReading('deleting', { updateTime: ago(51), now })).toMatchObject({ label: 'Deleting · stuck', severity: 'warning' })
    // Without the actor's last change Radar can't judge stuck: the chip stays in flight, never guesses.
    expect(actorStateReading('suspending', { now })).toEqual({ label: 'Suspending', severity: 'info' })
    // A resting state is never stuck, however old.
    expect(actorStateReading('suspended', { updateTime: ago(600), now })).toEqual({ label: 'Suspended', severity: 'neutral' })
    // A worker in trouble names itself first; an unavailable one is at risk (alert), a lost sandbox is lost.
    expect(actorStateReading('running', { impact: { kind: 'unavailable', reading: 'r' }, updateTime: ago(51), now })).toEqual({ label: 'Running · worker unavailable', severity: 'alert', title: 'r' })
    expect(actorStateReading('running', { impact: { kind: 'restarted', reading: 'r' } }).severity).toBe('error')
    expect(actorStateReading('running', { impact: { kind: 'draining', reading: 'r' } }).severity).toBe('warning')
  })

  it('gives the placement grid the badge’s tones, so its legend and the state bar agree', async () => {
    const { placementTone, workerImpactTone } = await import('./readings')
    for (const s of ['running', 'suspended', 'resuming', 'suspending', 'reverting', 'deleting', 'crashed', 'unknown'] as const) expect(placementTone(s, undefined)).toBe(actorStateTone(s))
    for (const k of ['unavailable', 'draining', 'restarted'] as const) expect(placementTone('running', k)).toBe(workerImpactTone(k))
    // Unavailable and draining read apart: orange and amber.
    expect(placementTone('running', 'unavailable')).toBe('alert')
    expect(placementTone('running', 'draining')).toBe('degraded')
  })

  it('counts the In progress segment over the four transitions, and leaves unread counts undefined', () => {
    const c = counts({ resuming: 6, suspending: 9, reverting: 2, deleting: 1, running: 3 })
    expect(segmentCount(c, 'inProgress')).toBe(18)
    expect(segmentCount(c, 'all')).toBe(21)
    expect(segmentCount(undefined, 'crashed')).toBeUndefined()
    expect(segmentOfState('reverting')).toBe('inProgress')
    expect(segmentOfState('crashed')).toBe('crashed')
    expect(segmentOfState(null)).toBe('all')
  })

  it('sends a link’s single transition inside In progress, and all four for the segment itself', () => {
    expect(segmentStates('inProgress', 'resuming')).toEqual(['resuming'])
    expect(segmentStates('inProgress', 'inProgress')).toEqual(['resuming', 'suspending', 'reverting', 'deleting'])
    expect(segmentStates('all', null)).toBeUndefined()
    expect(segmentStates('crashed', null)).toEqual(['crashed'])
  })

  it('draws one mark per actor up to the countable limit, then proportions', () => {
    const small = stateBarSegments(counts({ running: 3, crashed: 1 }))
    expect(small.countable).toBe(true)
    expect(small.segments.map((s) => s.state)).toEqual(['crashed', 'running'])
    const big = stateBarSegments(counts({ running: COUNTABLE_LIMIT, crashed: 1 }))
    expect(big.countable).toBe(false)
    expect(big.segments[0].share).toBeCloseTo(1 / (COUNTABLE_LIMIT + 1))
  })
})

describe('state reasons', () => {
  it('drops the state word the badge already shows', async () => {
    const { stateExtra } = await import('./readings')
    expect(stateExtra('resuming', 'Resuming for 36m')).toBe('for 36m')
    expect(stateExtra('suspended', 'Upload pending for 36m')).toBe('upload pending for 36m')
    expect(stateExtra('crashed', 'OOMKilled')).toBe('OOMKilled')
    expect(stateExtra('running', undefined)).toBeUndefined()
  })
})

describe('golden snapshots', () => {
  it('reads a build Substrate reports blocked as such', async () => {
    const { goldenReading } = await import('./readings')
    expect(goldenReading({ golden: { state: 'building', blocked: 'noMatchingPool' }, matchingPools: [], matchingPoolsComplete: true }).label).toBe('Can’t build: no pool can run it')
    expect(goldenReading({ golden: { state: 'building' }, matchingPools: null, matchingPoolsComplete: false }).label).toBe('Building')
    expect(goldenReading({ golden: { state: 'failed', error: 'x' }, matchingPools: [], matchingPoolsComplete: true }).label).toBe('Failed')
  })
})

describe('snapshots', () => {
  it('says what a resume does in a sentence that reads on its own', async () => {
    const { resumeSentence } = await import('./readings')
    expect(resumeSentence({ resumeGeneration: 3, newestSnapshot: { generation: 3, durable: 'completed' }, resumesWith: 'memory' })).toBe('Resumes where it left off: the snapshot holds its memory.')
    expect(resumeSentence({ resumeGeneration: 2, newestSnapshot: { generation: 3, durable: 'pending' }, resumesWith: 'memory' })).toContain('It resumes from generation 2: the newest is not stored yet.')
    expect(resumeSentence({ resumesWith: 'cold' })).toBe('Boots fresh: there is no snapshot to resume from.')
  })

  it('says how each fidelity resumes, and that no snapshot boots fresh', () => {
    expect(resumesWithText('memory')).toBe('resumes where it left off')
    expect(resumesWithText('filesystem')).toContain('saved files')
    expect(resumesWithText('cold')).toBe('boots fresh')
    expect(resumesWithText('unknown')).toContain('unknown')
  })

  it('names where an unstored snapshot’s files are, never calling it stored', () => {
    expect(durableText({ generation: 1, durable: 'completed' }).text).toBe('stored')
    expect(durableText({ generation: 1, durable: 'pending', locality: 'n1' })).toMatchObject({ text: 'upload pending', tone: 'degraded' })
    expect(durableText({ generation: 1, durable: 'none', locality: 'n1' }).text).toBe('only on n1')
    expect(durableText({ generation: 1, durable: 'none' }).text).toBe('not stored')
  })

  it('gives the exact kubectl ate revert command', () => {
    expect(revertCommand({ atespace: 'research', name: 'lit-review-0193' })).toBe('kubectl ate revert actor lit-review-0193 -a research')
  })
})

describe('API tier readings', () => {
  it('says what each failure means and what to do', () => {
    expect(tierReading({ state: 'failed', code: 'unreachable', reason: 'dial tcp: connection refused' })).toMatchObject({ title: 'API server unreachable', detail: 'dial tcp: connection refused', tone: 'unhealthy' })
    expect(tierReading({ state: 'failed', code: 'unauthenticated' }).nextStep).toContain('token')
    expect(tierReading({ state: 'failed', code: 'denied' }).nextStep).toContain('viewer role')
    expect(tierReading(sharedTier).detail).toContain('This Radar is shared')
    expect(tierReading({ state: 'notConfigured', code: 'notConfigured' }).title).toBe('Not connected')
  })

  it('names denied atespaces and a reached cap', () => {
    expect(coverageReason({ state: 'partial', deniedAtespaces: ['a', 'b'] }, 'actors')).toBe('Substrate denied atespaces a, b')
    expect(coverageReason({ state: 'partial', code: 'capExceeded', cap: 50000 }, 'actors')).toBe('Radar read the first 50000 actors and stopped there')
  })
})

describe('needs-attention severity', () => {
  it('reads the four steps in the house’s tones, and a banner in the same one', async () => {
    const { attentionTone, attentionBannerVariant, worstAttentionTone } = await import('./readings')
    expect((['critical', 'alert', 'warning', 'info'] as const).map((severity) => attentionTone({ severity }))).toEqual(['unhealthy', 'alert', 'degraded', 'neutral'])
    expect((['critical', 'alert', 'warning', 'info'] as const).map((severity) => attentionBannerVariant({ severity }))).toEqual(['error', 'alert', 'warning', 'info'])
    expect(worstAttentionTone([{ severity: 'warning' }, { severity: 'alert' }, { severity: 'info' }])).toBe('alert')
    expect(worstAttentionTone([])).toBeUndefined()
  })

  it('counts an alert row as needing attention, like any but info', () => {
    const data = workspace()
    const alert = { ...poolShortRow, id: 'workerUnavailable:w', rule: 'workerUnavailable' as const, severity: 'alert' as const, subject: { kind: 'Worker' as const, namespace: 'ns', name: 'w' } }
    expect(substrateAttentionCount({ ...data, needsAttention: [...data.needsAttention, alert] }).count).toBe(4)
    expect(substrateScreenCounts({ ...data, needsAttention: [...data.needsAttention, alert] }).workers.count).toBe(1)
  })
})

describe('needs-attention counts', () => {
  it('counts every row, and each screen its own', () => {
    const data = workspace()
    expect(substrateAttentionCount(data)).toMatchObject({ count: 3, lowerBound: false })
    const screens = substrateScreenCounts(data)
    expect(screens.actors).toMatchObject({ count: 14, title: '14 actors need attention: 14 crashed, 0 stuck in a transition.' })
    expect(screens.templates.count).toBe(1)
    expect(screens.pools.count).toBe(1)
    expect(screens.workers.count).toBe(0)
  })

  it('reads unknown, not zero, while Substrate’s API is not connected', () => {
    const data = notConnectedWorkspace()
    expect(substrateAttentionCount(data).count).toBeNull()
    const screens = substrateScreenCounts(data)
    expect(screens.actors.count).toBeNull()
    expect(screens.actors.title).toContain('not connected')
    // Pools come from Kubernetes too: a short pool still counts, as a lower bound.
    const short = { ...data, needsAttention: [poolShortRow] }
    expect(substrateScreenCounts(short).pools).toMatchObject({ count: 1, lowerBound: true })
  })

  it('makes a count over a partial read a lower bound', () => {
    const data = workspace({ coverage: { ...workspace().coverage!, actors: { state: 'partial', deniedAtespaces: ['restricted'] } } })
    expect(substrateAttentionCount(data)).toMatchObject({ count: 3, lowerBound: true })
    expect(substrateScreenCounts(data).actors.title).toMatch(/^At least 14 actors need attention/)
  })
})

describe('routes', () => {
  it('parses screens and pages, and builds pinned page links', () => {
    expect(parseSubstrateRoute('/substrate')).toEqual({ screen: 'overview' })
    expect(parseSubstrateRoute('/substrate/actors')).toEqual({ screen: 'actors' })
    expect(parseSubstrateRoute('/substrate/actors/research/lit-review-0193')).toEqual({ screen: 'actors', actor: { atespace: 'research', name: 'lit-review-0193' } })
    expect(parseSubstrateRoute('/substrate/workerpools/agent-ns/agent-pool').pool).toEqual({ namespace: 'agent-ns', name: 'agent-pool' })
    expect(substrateActorPath({ atespace: 'r', name: 'a' }, 'kind-x', 'logs')).toBe('/substrate/actors/r/a?ctx=kind-x&tab=logs')
    expect(substrateActorsPath({ state: 'crashed', template: 'r/t' })).toBe('/substrate/actors?state=crashed&template=r%2Ft')
  })

  it('round-trips the actor drawer parameter and rejects a partial one', () => {
    expect(decodeActorParam(encodeActorParam({ atespace: 'r', name: 'a' }))).toEqual({ atespace: 'r', name: 'a' })
    expect(decodeActorParam('r/')).toBeNull()
    expect(decodeActorParam(null)).toBeNull()
  })
})

describe('text', () => {
  it('never renders an unread count as zero', () => {
    expect(countText(undefined)).toBe('unknown')
    expect(countText(0)).toBe('0')
    expect(countText(1284)).toBe('1,284')
  })

  it('writes resources as Kubernetes does', () => {
    expect(bytesText(2 * 1024 ** 3)).toBe('2Gi')
    expect(bytesText(512 * 1024 ** 2)).toBe('512Mi')
    expect(cpuText(2000)).toBe('2')
    expect(cpuText(500)).toBe('500m')
  })
})

describe('the Connect form', () => {
  it('saves only what was chosen: no token path for the shared identity, no empty optional fields', async () => {
    const { connectionUpdate } = await import('./SubstrateConnect')
    const target = { binding: 'b', context: 'kind-demo', fingerprint: 'f', clientGeneration: 1, operationGeneration: 2 }
    expect(connectionUpdate({ identity: 'shared', tokenFile: '/tmp/t', atespaces: '', endpoint: ' ', caFile: '', serverName: '' }, target)).toEqual({ target, settings: { identity: 'shared', tokenFile: undefined, atespaces: undefined, endpoint: undefined, caFile: undefined, serverName: undefined } })
    expect(connectionUpdate({ identity: 'tokenFile', tokenFile: ' ~/t ', atespaces: 'research, , support-bots', endpoint: 'h:443', caFile: '/ca.pem', serverName: 'api' }, target)).toEqual({ target, settings: { identity: 'tokenFile', tokenFile: '~/t', atespaces: ['research', 'support-bots'], endpoint: 'h:443', caFile: '/ca.pem', serverName: 'api' } })
  })
})

describe('a pool’s read failure', () => {
  it('says an incomplete pool read can’t tell, and only notFound says the pool is gone', () => {
    expect(poolReadFailure('incomplete', 'WorkerPools syncing')).toBe('Radar can’t tell whether this pool exists: it read too little of the WorkerPools or of Substrate’s workers (WorkerPools syncing).')
    expect(poolReadFailure('notFound', undefined)).toContain('There is no WorkerPool by this name')
    expect(poolReadFailure('unreachable', 'dial tcp')).toBe('Radar couldn’t read the pool from Substrate’s API: dial tcp.')
  })
})

describe('times, readings and evidence', () => {
  it('says every time relatively, past and ahead, never as a timestamp', () => {
    const now = Date.parse('2026-11-18T03:00:00Z')
    expect(relativeTime('2026-11-18T02:48:00Z', now)).toBe('12m ago')
    expect(relativeTime('2026-11-18T03:04:00Z', now)).toBe('in 4m')
    expect(relativeTime('2026-11-18T02:59:58Z', now)).toBe('just now')
    expect(relativeTime('2026-11-16T03:00:00Z', now)).toBe('2d ago')
    // The shared formatter's day starts at 24h, as Go's FormatAgeShort does.
    expect(relativeTime('2026-11-16T15:00:00Z', now)).toBe('1d ago')
  })

  it('shows a reading’s first sentence whole and keeps the rest for Details', () => {
    expect(splitReading('Substrate places no new actors on it. 3 actors are still assigned.')).toEqual(['Substrate places no new actors on it.', '3 actors are still assigned.'])
    expect(splitReading('One sentence only.')).toEqual(['One sentence only.', undefined])
    expect(splitReading('Version 2.5 of the image is pinned. It never updates.')).toEqual(['Version 2.5 of the image is pinned.', 'It never updates.'])
  })

  it('reads the newest container line as evidence the actor still runs', () => {
    const now = Date.parse('2026-11-18T03:00:00Z')
    expect(servingEvidence(undefined, now)).toBeUndefined()
    expect(servingEvidence([{ kind: 'worker', time: '2026-11-18T02:59:50Z' }], now)).toEqual({ recent: false, at: undefined })
    expect(servingEvidence([{ kind: 'container', time: '2026-11-18T02:40:00Z' }, { kind: 'container', time: '2026-11-18T02:59:48Z' }], now)).toEqual({ recent: true, at: '2026-11-18T02:59:48Z' })
    expect(servingEvidence([{ kind: 'container', time: '2026-11-18T02:40:00Z' }], now)?.recent).toBe(false)
  })
})

describe('pool reading over a partial read of the workers', () => {
  it('says the active count is a floor when not every worker was read', async () => {
    const { poolReading } = await import('./readings')
    const placement = { workers: { total: 2, active: 2, draining: 0, unavailable: 0 }, slots: { used: 1, total: 8 }, cpuMillicores: { used: 1, total: 2 }, memoryBytes: { used: 1, total: 2 } }
    const kubernetes = { namespace: 'a', name: 'p', desired: 5, ready: 2, health: 'degraded' as const, healthReason: 'Scaling' }
    expect(poolReading({ capacity: 'short', placement: { ...placement, complete: false }, kubernetes }).label).toBe('Short of workers: at least 2 of 5 active')
    expect(poolReading({ capacity: 'short', placement: { ...placement, complete: true }, kubernetes }).label).toBe('Short of workers: 2 of 5 active')
  })
})

describe('one fact once, and a worker that says what happened to its actors', () => {
  it('drops a reading’s sentence that its next step already says', async () => {
    const { withoutRepeats } = await import('./readings')
    expect(withoutRepeats('Placed and checkpointing on its worker. Substrate continues an unfinished suspend only on the next suspend call.', 'Suspend it again: Substrate continues an unfinished suspend only on the next suspend call.'))
      .toBe('Placed and checkpointing on its worker.')
    expect(withoutRepeats('Same as the step.', 'Same as the step.')).toBeUndefined()
    expect(withoutRepeats('Unrelated.', 'Do this.')).toBe('Unrelated.')
    // The same fact in other words is still the same fact.
    expect(withoutRepeats('Checkpointing on worker w-1. An unfinished suspend continues only on the next suspend call. 3 other actors are in the same state.', 'Suspend it again: Substrate continues an unfinished suspend only on the next suspend call.'))
      .toBe('Checkpointing on worker w-1. 3 other actors are in the same state.')
  })

  it('reads a reconciling worker’s lost sandboxes in its chip’s red, the count left to the line beside it, and a terminating Pod as terminating', async () => {
    const { workerStateReading, workerPodText } = await import('./readings')
    const reading = '3 actors lost their sandboxes when its process restarted; Substrate marks them crashed when it reconciles the worker.'
    // Lost sandboxes are critical, the tone of their Needs attention row; a reconcile that lost none stays amber.
    expect(workerStateReading({ state: 'active', workerState: { kind: 'reconciling', assigned: 13, lost: 3, reading } })).toMatchObject({ label: 'Active · reconciling', severity: 'error', title: reading })
    expect(workerStateReading({ state: 'active', workerState: { kind: 'reconciling', assigned: 13, lost: 0, reading } })).toMatchObject({ severity: 'warning' })
    // An unavailable worker is at risk, not lost: orange, as its row reads.
    expect(workerStateReading({ state: 'unavailable' }).severity).toBe('alert')
    expect(workerStateReading({ state: 'unavailable', workerState: { kind: 'reconciling', reading } }).severity).toBe('alert')
    // Actors not read: no count is made up.
    expect(workerStateReading({ state: 'active', workerState: { kind: 'reconciling', reading } }).label).toBe('Active · reconciling')
    expect(workerStateReading({ state: 'active' })).toMatchObject({ label: 'Active', severity: 'success' })
    expect(workerStateReading({ state: 'draining', workerState: { kind: 'draining', assigned: 4, reading: 'Draining…' } })).toMatchObject({ label: 'Draining', title: 'Draining…' })
    const { withoutReasonCode } = await import('./readings')
    expect(withoutReasonCode('GoldenActorCrashed: golden actor crashed before its snapshot was taken')).toBe('golden actor crashed before its snapshot was taken')
    expect(withoutReasonCode('killed for exceeding its memory limit')).toBe('killed for exceeding its memory limit')
    expect(workerPodText({ podReady: true, podTerminating: true })).toBe('Pod terminating')
    expect(workerPodText({ podReady: false })).toBe('Pod not ready')
  })
})

describe('tier readings for a Radar Hub owner, who has no form', () => {
  const owner = { mode: 'sharedAsCaller' as const, principal: 'system:serviceaccount:ate-system:ate-client' }
  const noForm = /Advanced|another identity|this machine|Change connection|choose|save/i

  it('says the shared identity is missing, with a kubectl check', () => {
    const r = tierReading({ state: 'failed', code: 'notInstalled', identity: owner, reason: 'ServiceAccount ate-system/ate-client does not exist.' })
    expect(r).toMatchObject({ detail: 'Substrate’s shared client identity (ate-system/ate-client) doesn’t exist here.', nextStep: 'Check that Substrate is installed in ate-system.', command: 'kubectl -n ate-system get sa ate-client' })
    expect(`${r.detail} ${r.nextStep}`).not.toMatch(noForm)
  })

  it('says Radar couldn’t reach or verify the API server from inside the cluster, with the server’s reason', () => {
    const tls = tierReading({ state: 'failed', code: 'tls', identity: owner, reason: 'x509: certificate signed by unknown authority' })
    expect(tls.detail).toBe('Radar couldn’t verify api.ate-system.svc:443 from inside the cluster. x509: certificate signed by unknown authority')
    expect(tls.command).toBe('kubectl -n ate-system get svc api')
    const down = tierReading({ state: 'failed', code: 'unreachable', identity: owner, reason: 'dial tcp: connection refused' })
    expect(down.detail).toBe('Radar couldn’t reach api.ate-system.svc:443 from inside the cluster. dial tcp: connection refused')
    expect(down.command).toBe('kubectl -n ate-system get pods')
    for (const r of [tls, down]) expect(`${r.detail} ${r.nextStep}`).not.toMatch(noForm)
  })

  it('says Substrate refused the shared identity and that an operator must grant it', () => {
    for (const code of ['unauthenticated', 'denied'] as const) {
      const r = tierReading({ state: 'failed', code, identity: owner })
      expect(r.title).toBe('Substrate refused the shared identity')
      expect(r.detail).toContain('it isn’t granted in this install')
      expect(r.nextStep).toContain('Substrate operator to grant it')
      expect(r.nextStep).toContain('this Radar can’t read Substrate as it stands')
      expect(`${r.detail} ${r.nextStep}`).not.toMatch(noForm)
    }
  })

  it('leaves a local Radar’s readings, which point at its form, exactly as they were', () => {
    expect(tierReading({ state: 'failed', code: 'notInstalled' })).toEqual({ title: 'API server not found', tone: 'unknown', detail: 'Radar has not read Substrate’s API.', nextStep: 'Check that Substrate’s API server runs as Service ate-system/api, or set its endpoint under Advanced.' })
    expect(tierReading({ state: 'failed', code: 'tls' }).nextStep).toBe('Check the CA file and server name under Advanced, or that the cluster’s trust bundle for Substrate exists.')
    expect(tierReading({ state: 'failed', code: 'unreachable', reason: 'dial tcp' })).toMatchObject({ title: 'API server unreachable', detail: 'dial tcp' })
    expect(tierReading({ state: 'failed', code: 'unauthenticated' }).nextStep).toBe('Check that the token is current, or choose another identity.')
    expect(tierReading({ state: 'failed', code: 'denied' }).nextStep).toBe('Ask a Substrate owner to grant this identity the viewer role, or choose another identity.')
  })
})
