import { describe, expect, it } from 'vitest'
import type { CNPGClusterHA } from '@skyhook-io/k8s-ui'
import type { CNPGClusterFacts, CNPGRuntimeResponse } from '../../../api/cnpg'
import { CNPG_OP_STALL_MS, advanceCNPGOperation, restartedSince, summarizeSteps, supersedeFor, type CNPGObservation, type CNPGTrackedOperation } from './model'

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
    currentPrimary: 'pg-1',
    targetPrimary: 'pg-1',
    phase: 'Cluster in healthy state',
    hibernation: '',
    hibernated: false,
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

const obs = (o: Partial<CNPGObservation>): CNPGObservation => ({ now: T0 + 30_000, clusterUID: 'uid-1', ...o })

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
    const obs: CNPGObservation = { now: T0 + 60_000, facts: facts({ phase: 'Creating a new replica', instances: [inst('pg-1', true), inst('pg-2', true), inst('pg-4', false)] }) }
    const next = advanceCNPGOperation(destroy, obs)
    expect(next.state).toBe('progressing')
    expect(next.steps?.[0].done).toBe(true)
    expect(next.steps?.[1].label).toContain('pg-4')
  })

  it('never completes while the destroyed instance is still there', () => {
    const obs: CNPGObservation = { now: T0 + 60_000, facts: facts({ instances: [inst('pg-1', true), inst('pg-2', true), inst('pg-3', true)] }) }
    expect(advanceCNPGOperation(destroy, obs).state).not.toBe('completed')
  })
})
