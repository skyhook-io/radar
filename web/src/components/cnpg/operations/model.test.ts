import { describe, expect, it } from 'vitest'
import type { CNPGClusterHA } from '@skyhook-io/k8s-ui'
import type { CNPGClusterFacts, CNPGRuntimeResponse } from '../../../api/cnpg'
import { CNPG_OP_STALL_MS, CNPG_OP_UNOBSERVABLE_MS, advanceCNPGOperation, restartedSince, summarizeSteps, supersedeFor, type CNPGObservation, type CNPGTrackedOperation } from './model'

const T0 = Date.parse('2026-09-30T12:00:00Z')

function op(over: Partial<CNPGTrackedOperation>): CNPGTrackedOperation {
  return {
    id: 'op',
    kind: 'switchover',
    label: 'Switchover to pg-2',
    context: 'kind',
    namespace: 'db',
    cluster: 'pg',
    clusterUID: 'uid-1',
    target: { name: 'pg-2' },
    startedAt: T0,
    baseline: { currentPrimary: 'pg-1' },
    state: 'requested',
    lastProgressAt: T0,
    ...over,
  }
}

function facts(over: Partial<CNPGClusterFacts>): CNPGClusterFacts {
  return {
    generation: 0,
    currentPrimary: 'pg-1',
    targetPrimary: 'pg-1',
    phase: 'Cluster in healthy state',
    hibernation: '',
    hibernated: false,
    archivingFailing: false,
    fencedInstances: { raw: '', all: false, instances: [] },
    instances: [],
    backupMethods: [],
    isReplicaCluster: false,
    terminating: false,
    maintenance: { declared: false, inProgress: false, reusePVC: true },
    ...over,
  }
}

function runtime(primary: string, standbys: { pod: string; state: string }[]): CNPGRuntimeResponse {
  return {
    cluster: { namespace: 'db', name: 'pg', uid: 'uid-1' },
    sampledAt: '',
    permission: { proxy: 'allowed' },
    instances: [
      { pod: primary, role: 'primary', status: { state: 'ok', replication: standbys.map((s) => ({ applicationName: s.pod, state: s.state })) }, metrics: { state: 'ok' } },
      ...standbys.map((s) => ({ pod: s.pod, role: 'replica' as const, status: { state: 'ok' as const }, metrics: { state: 'ok' as const } })),
    ],
  }
}

function ha(over: Partial<CNPGClusterHA>): CNPGClusterHA {
  return {
    cluster: { namespace: 'db', name: 'pg', uid: 'uid-1' },
    sampledAt: '',
    instances: [],
    pods: { state: 'ok' },
    nodes: { state: 'ok' },
    quorum: { enabled: false, object: { state: 'notFound' } },
    pdbs: { state: 'ok', enabled: true, items: [] },
    primaryLease: { state: 'notFound' },
    operatorLease: { state: 'notFound' },
    jobs: { state: 'ok', items: [] },
    rwEndpoints: { state: 'ok', service: 'pg-rw', pods: [] },
    certificates: [],
    maintenance: { declared: false, inProgress: false, reusePVC: true },
    ...over,
  }
}

const fresh = { updatedAt: T0 + 30_000, failed: false, clusterUID: 'uid-1' }
const obs = (o: Partial<CNPGObservation>): CNPGObservation => ({
  now: T0 + 30_000,
  clusterUID: 'uid-1',
  freshness: { clusterUID: fresh, facts: fresh, cluster: fresh, ha: fresh, runtime: fresh, backups: fresh },
  ...o,
})

describe('switchover observer', () => {
  it('walks from observed to completed only when the old primary streams and rw points at the target', () => {
    let o = advanceCNPGOperation(op({}), obs({ facts: facts({ targetPrimary: 'pg-2', phase: 'Switchover in progress' }) }))
    expect(o.state).toBe('observed')

    o = advanceCNPGOperation(o, obs({ facts: facts({ currentPrimary: 'pg-2', targetPrimary: 'pg-2' }), runtime: runtime('pg-2', [{ pod: 'pg-1', state: 'startup' }]), ha: ha({ rwEndpoints: { state: 'ok', service: 'pg-rw', pods: ['pg-2'] } }) }))
    expect(o.state).toBe('progressing')

    o = advanceCNPGOperation(o, obs({ facts: facts({ currentPrimary: 'pg-2', targetPrimary: 'pg-2' }), runtime: runtime('pg-2', [{ pod: 'pg-1', state: 'streaming' }]), ha: ha({ rwEndpoints: { state: 'ok', service: 'pg-rw', pods: ['pg-2'] } }) }))
    expect(o.state).toBe('completed')
    expect(o.steps?.every((s) => s.done)).toBe(true)
  })

  it('is unobservable, not completed or stalled, when the rejoin cannot be read', () => {
    const denied = { ...runtime('pg-2', []), permission: { proxy: 'denied' as const } }
    const o = advanceCNPGOperation(op({}), obs({ now: T0 + CNPG_OP_STALL_MS * 3, facts: facts({ currentPrimary: 'pg-2', targetPrimary: 'pg-2' }), runtime: denied }))
    expect(o.state).toBe('unobservable')
    expect(o.detail).toContain('runtime access')
  })

  it('fails when a different instance became primary', () => {
    const o = advanceCNPGOperation(op({}), obs({ facts: facts({ currentPrimary: 'pg-3', targetPrimary: 'pg-3' }) }))
    expect(o.state).toBe('failed')
  })

  it('stalls only with telemetry showing no movement', () => {
    const f = facts({ targetPrimary: 'pg-2', phase: 'Switchover in progress' })
    let o = advanceCNPGOperation(op({}), obs({ facts: f }))
    o = advanceCNPGOperation(o, obs({ now: T0 + CNPG_OP_STALL_MS + 60_000, facts: f }))
    expect(o.state).toBe('stalled')
  })

  it('is superseded when the Cluster was recreated', () => {
    const o = advanceCNPGOperation(op({}), obs({ clusterUID: 'uid-2', facts: facts({}) }))
    expect(o.state).toBe('superseded')
  })

  it.each([
    undefined,
    { updatedAt: T0 - 1, failed: false },
    { updatedAt: T0 + 30_000, failed: true },
  ])('withholds an unverified replacement UID (%j)', (clusterUID) => {
    const o = advanceCNPGOperation(op({}), obs({ clusterUID: 'uid-2', freshness: { clusterUID } }))
    expect(o.state).not.toBe('superseded')
    expect(o.finishedAt).toBeUndefined()
  })
})

describe('restart evidence', () => {
  it('counts a recreated Pod, a restarted container or a moved postmaster start', () => {
    const since = T0
    const h = ha({ instances: [{ pod: 'pg-1', podUID: 'new', role: 'primary', ready: true, restartCount: 0, podCreatedAt: '2026-09-30T11:00:00Z', postgresStartedAt: '2026-09-30T11:00:00Z' }] })
    expect(restartedSince(obs({ ha: h }), 'pg-1', since, 'old')).toBe(true)
    expect(restartedSince(obs({ ha: h }), 'pg-1', since, 'new')).toBe(false)
    const rt = runtime('pg-1', [])
    rt.instances[0].metrics.postmasterStartTime = (T0 + 5_000) / 1000
    expect(restartedSince(obs({ ha: h, runtime: rt }), 'pg-1', since, 'new')).toBe(true)
    expect(restartedSince(obs({}), 'pg-1', since)).toBeNull()
  })

  it('completes a rolling restart when every instance restarted and is ready', () => {
    const h = ha({
      instances: [
        { pod: 'pg-1', podUID: 'a', role: 'primary', ready: true, restartCount: 0, postgresStartedAt: '2026-09-30T12:01:00Z' },
        { pod: 'pg-2', podUID: 'b2', role: 'replica', ready: true, restartCount: 0 },
      ],
    })
    const o = advanceCNPGOperation(op({ kind: 'restart', baseline: { instances: ['pg-1', 'pg-2'], podUIDs: { 'pg-1': 'a', 'pg-2': 'b' } } }), obs({ ha: h, facts: facts({}) }))
    expect(o.state).toBe('completed')
  })
})

describe('backup observer', () => {
  const b = (phase?: string) => [{ metadata: { name: 'pg-1', namespace: 'db' }, status: phase ? { phase } : {} }]
  it('follows the Backup phase and never guesses without access', () => {
    const base = op({ kind: 'backup', target: { name: 'pg-1' } })
    expect(advanceCNPGOperation(base, obs({ backups: b('running') })).state).toBe('progressing')
    expect(advanceCNPGOperation(base, obs({ backups: b('completed') })).state).toBe('completed')
    expect(advanceCNPGOperation(base, obs({ backups: b('failed') })).state).toBe('failed')
    expect(advanceCNPGOperation(base, obs({ backups: undefined })).state).toBe('unobservable')
  })
})

describe('reload', () => {
  it('says there is no completion signal', () => {
    const o = advanceCNPGOperation(op({ kind: 'reload' }), obs({}))
    expect(o.state).toBe('unobservable')
    expect(o.detail).toContain('Nothing in the cluster reports')
  })
})

describe('supersedeFor', () => {
  it('marks a conflicting unfinished operation on the same cluster', () => {
    const older = op({ id: 'a' })
    const next = op({ id: 'b', kind: 'hibernate', label: 'Hibernate' })
    expect(supersedeFor([older], next)[0].state).toBe('superseded')
    expect(supersedeFor([op({ id: 'c', cluster: 'other' })], next)[0].state).toBe('requested')
  })
})

describe('summarizeSteps', () => {
  it('never completes past an unobservable step', () => {
    expect(summarizeSteps([{ label: 'a', done: true }, { label: 'b', done: null }])).toBe('unobservable')
    expect(summarizeSteps([{ label: 'a', done: true }, { label: 'b', done: false }])).toBe('progressing')
    expect(summarizeSteps([{ label: 'a', done: false }])).toBe('requested')
  })
})

describe('destroyInstance observer', () => {
  const inst = (pod: string, ready: boolean) => ({ pod, podUID: `uid-${pod}`, role: 'standby' as const, ready, healthy: ready, fenced: false, podReadable: true, podExists: true })
  const destroy = op({ kind: 'destroyInstance', target: { name: 'pg-3', uid: 'uid-pg-3' }, baseline: { instances: ['pg-1', 'pg-2', 'pg-3'] } })

  it('stays open until a replacement instance exists and is ready', () => {
    const next = advanceCNPGOperation(destroy, obs({ now: T0 + 60_000, facts: facts({ phase: 'Creating a new replica', instances: [inst('pg-1', true), inst('pg-2', true), inst('pg-4', false)] }) }))
    expect(next.state).toBe('progressing')
    expect(next.steps?.[0].done).toBe(true)
    expect(next.steps?.[1].label).toContain('pg-4')
  })

  it('never completes while the destroyed instance is still there', () => {
    expect(advanceCNPGOperation(destroy, obs({ now: T0 + 60_000, facts: facts({ instances: [inst('pg-1', true), inst('pg-2', true), inst('pg-3', true)] }) })).state).not.toBe('completed')
  })
})

describe('operations Radar cannot observe', () => {
  it('a reload finishes at once instead of being followed all session', () => {
    const next = advanceCNPGOperation(op({ kind: 'reload', target: undefined }), obs({ now: T0 + 1000 }))
    expect(next.state).toBe('unobservable')
    expect(next.finishedAt).toBe(T0 + 1000)
  })

  it('anything else unobservable is left alone after the follow window', () => {
    const early = advanceCNPGOperation(op({ kind: 'switchover' }), { now: T0 + 60_000 })
    expect(early.finishedAt).toBeUndefined()
    const late = advanceCNPGOperation(op({ kind: 'switchover' }), { now: T0 + 16 * 60_000 })
    expect(late.state).toBe('unobservable')
    expect(late.finishedAt).toBe(T0 + 16 * 60_000)
  })

  it.each(['reload', 'switchover'])('keeps an old %s operation open while its identity read is in flight', (kind) => {
    const now = T0 + CNPG_OP_UNOBSERVABLE_MS + 1
    const pending = advanceCNPGOperation(op({ kind }), { now, identityPending: true })
    expect(pending.state).toBe('unobservable')
    expect(pending.detail).toContain('identity')
    expect(pending.finishedAt).toBeUndefined()
    const replaced = advanceCNPGOperation(pending, obs({ now: now + 1, clusterUID: 'uid-2' }))
    expect(replaced.state).toBe('superseded')
    expect(replaced.finishedAt).toBe(now + 1)
  })

  it('bounds tracking after an unavailable identity read has settled', () => {
    const now = T0 + CNPG_OP_UNOBSERVABLE_MS + 1
    const unknown = advanceCNPGOperation(op({ kind: 'reload' }), { now, identityPending: false })
    expect(unknown.state).toBe('unobservable')
    expect(unknown.detail).toContain('identity')
    expect(unknown.finishedAt).toBe(now)
  })
})

describe('fence observer', () => {
  const inst = (pod: string, ready: boolean) => ({ pod, podUID: `uid-${pod}`, role: 'standby' as const, ready, healthy: ready, fenced: true, podReadable: true, podExists: true })
  const fenced = facts({ fencedInstances: { raw: '["pg-2"]', all: false, instances: ['pg-2'] }, instances: [inst('pg-2', false)] })
  const fence = () => op({ kind: 'fence', label: 'Fence pg-2', target: undefined, baseline: { instances: ['pg-2'] } })
  const withStatus = (status: Record<string, unknown>) => {
    const rt = runtime('pg-1', [])
    rt.instances.push({ pod: 'pg-2', role: 'replica', status, metrics: { state: 'ok' } } as never)
    return rt
  }

  it('never claims PostgreSQL stopped: CloudNativePG reports no shutdown signal', () => {
    for (const status of [{ state: 'ok' }, { state: 'ok', mightBeUnavailable: true }, { state: 'partial' }]) {
      const o = advanceCNPGOperation(fence(), obs({ facts: fenced, runtime: withStatus(status) }))
      expect(o.state).not.toBe('completed')
      expect(o.steps?.find((x) => x.label.includes('PostgreSQL stopped'))?.done).toBeNull()
    }
  })
  it('stops following once the fence is recorded, as unverified', () => {
    const o = advanceCNPGOperation(fence(), obs({ facts: fenced }))
    expect(o.state).toBe('unobservable')
    expect(o.steps?.[0].done).toBe(true)
    expect(o.finishedAt).toBeDefined()
  })
  it('keeps following until the fence annotation lands', () => {
    const o = advanceCNPGOperation(fence(), obs({ facts: facts({ instances: [inst('pg-2', false)] }) }))
    expect(o.steps?.[0].done).toBe(false)
    expect(o.finishedAt).toBeUndefined()
  })
})

describe('stale observation sources', () => {
  const promoted = () => ({
    facts: facts({ currentPrimary: 'pg-2', targetPrimary: 'pg-2' }),
    runtime: runtime('pg-2', [{ pod: 'pg-1', state: 'streaming' }]),
    ha: ha({ rwEndpoints: { state: 'ok', service: 'pg-rw', pods: ['pg-2'] } }),
  })
  const now = T0 + 30_000

  it('an hour-old endpoints answer whose refresh failed cannot certify a switchover', () => {
    const o = advanceCNPGOperation(
      op({}),
      obs({
        ...promoted(),
        freshness: {
          clusterUID: fresh,
          facts: { updatedAt: now, failed: false, clusterUID: 'uid-1' },
          runtime: { updatedAt: now, failed: false, clusterUID: 'uid-1' },
          ha: { updatedAt: T0 - 3_600_000, failed: true },
        },
      }),
    )
    expect(o.state).not.toBe('completed')
    expect(o.steps?.find((s) => s.label.startsWith('Read-write Service'))?.done).toBeNull()
  })
  it('an answer from before the action is withheld even when its refresh did not fail', () => {
    const o = advanceCNPGOperation(
      op({}),
      obs({ ...promoted(), freshness: { clusterUID: fresh, facts: { updatedAt: now, failed: false, clusterUID: 'uid-1' }, runtime: { updatedAt: now, failed: false, clusterUID: 'uid-1' }, ha: { updatedAt: T0 - 1, failed: false } } }),
    )
    expect(o.state).not.toBe('completed')
  })
  it('fresh sources still complete it', () => {
    const o = advanceCNPGOperation(op({}), obs({ ...promoted(), freshness: { clusterUID: fresh, facts: fresh, runtime: fresh, ha: fresh } }))
    expect(o.state).toBe('completed')
  })

  it.each(['facts', 'ha', 'runtime'] as const)('a fresh %s response from another Cluster cannot certify completion', (source) => {
    const observation = obs({ ...promoted() })
    if (source === 'ha') observation.ha!.cluster.uid = 'another-uid'
    if (source === 'runtime') observation.runtime!.cluster.uid = 'another-uid'
    observation.freshness = { ...observation.freshness, [source]: { ...fresh, clusterUID: 'another-uid' } }
    const result = advanceCNPGOperation(op({}), observation)
    expect(result.state).not.toBe('completed')
    expect(result.finishedAt).toBeUndefined()
  })
  it('a source with no freshness record is withheld', () => {
    const o = advanceCNPGOperation(op({}), { ...obs(promoted()), freshness: undefined })
    expect(o.state).not.toBe('completed')
  })
})

describe('switchover without readable endpoints', () => {
  it('never completes while the read-write Service is unverified', () => {
    const o = advanceCNPGOperation(
      op({}),
      obs({
        facts: facts({ currentPrimary: 'pg-2', targetPrimary: 'pg-2' }),
        runtime: runtime('pg-2', [{ pod: 'pg-1', state: 'streaming' }]),
        ha: ha({ rwEndpoints: { state: 'denied', grant: { verb: 'list', group: 'discovery.k8s.io', resource: 'endpointslices', namespace: 'db' }, service: 'pg-rw', pods: [] } }),
      }),
    )
    expect(o.state).toBe('unobservable')
    const step = o.steps?.find((s) => s.label.startsWith('Read-write Service'))
    expect(step?.done).toBeNull()
    expect(o.detail).toContain('read-write Service is unverified')
  })
})

describe('unread readiness after an observed restart', () => {
  it.each(['restart', 'restartInstance'])('keeps %s unobservable rather than stalled', (kind) => {
    const rt = runtime('pg-1', [])
    rt.instances[0].metrics.postmasterStartTime = (T0 + 1000) / 1000
    const operation = op({ kind, target: { name: 'pg-1' }, baseline: { instances: ['pg-1'] } })
    const result = advanceCNPGOperation(operation, obs({ runtime: rt }))
    expect(result.steps?.[0].done).toBeNull()
    expect(result.state).toBe('unobservable')
    expect(advanceCNPGOperation(result, obs({ now: T0 + CNPG_OP_STALL_MS + 1, runtime: rt })).state).toBe('unobservable')
  })
})

it('supersedes a scheduled run when its source Cluster is recreated', () => {
  const operation = op({ kind: 'run', target: { name: 'manual-backup' } })
  const result = advanceCNPGOperation(operation, obs({ clusterUID: 'replacement', backups: [{ metadata: { name: 'manual-backup', namespace: 'db' }, status: { phase: 'completed' } }] }))
  expect(result.state).toBe('superseded')
})

it('does not certify a backup whose source identity was never recorded', () => {
  const result = advanceCNPGOperation(op({ kind: 'run', clusterUID: undefined, target: { name: 'manual-backup' } }), obs({ backups: [{ metadata: { name: 'manual-backup', namespace: 'db' }, status: { phase: 'completed' } }] }))
  expect(result.state).toBe('unobservable')
})
