import { describe, expect, it } from 'vitest'
import { advanceCNPGOperation, type CNPGTrackedOperation } from './model'
import { cnpgOperationHandoff, cnpgOperationsForCluster, latestCNPGOperation } from './presentation'

const operation: CNPGTrackedOperation = {
  id: 'local',
  kind: 'reload',
  label: 'Reload configuration',
  context: 'kind-db',
  namespace: 'db',
  cluster: 'pg',
  clusterUID: 'uid',
  startedAt: Date.parse('2026-10-07T00:00:00Z'),
  lastProgressAt: 0,
  state: 'requested',
  baseline: { secret: 'must not be copied' },
}

describe('local operation presentation', () => {
  it('keeps completed predecessor operations out of the header, count and handoff list', () => {
    const completed = { ...operation, state: 'completed' as const, finishedAt: operation.startedAt + 1000 }
    const subject = { context: 'kind-db', namespace: 'db', name: 'pg', uid: 'replacement' }
    expect(cnpgOperationsForCluster([completed], subject)).toEqual([])
    const current = { ...completed, id: 'replacement-restore', clusterUID: 'replacement', startedAt: completed.startedAt + 2000 }
    expect(cnpgOperationsForCluster([completed, current], subject)).toEqual([current])
    expect(latestCNPGOperation([completed, current], subject)).toBe(current)
  })

  it('never joins a record across contexts, namespaces or a recreated Cluster', () => {
    const subject = { context: 'kind-db', namespace: 'db', name: 'pg', uid: 'uid' }
    expect(latestCNPGOperation([operation], subject)).toBe(operation)
    for (const changed of [{ context: 'prod' }, { namespace: 'elsewhere' }, { uid: 'new-uid' }]) {
      expect(latestCNPGOperation([operation], { ...subject, ...changed })).toBeUndefined()
    }
    expect(
      latestCNPGOperation([operation, { ...operation, id: 'newer', startedAt: operation.startedAt + 1000 }], subject)
        ?.id,
    ).toBe('newer')
  })

  it('records a check even when the result is unobservable; terminal records retain their assessment time', () => {
    const checked = advanceCNPGOperation(operation, { now: operation.startedAt + 1000 })
    expect(checked.state).toBe('unobservable')
    expect(checked.lastCheckedAt).toBe(operation.startedAt + 1000)
    const terminal = { ...checked, state: 'completed' as const }
    expect(advanceCNPGOperation(terminal, { now: operation.startedAt + 2000 })).toBe(terminal)
  })

  it('copies verification intent and a pinned live link without the private baseline', () => {
    const text = cnpgOperationHandoff(
      {
        ...operation,
        steps: [
          { label: 'Verify uploads', done: null },
          { label: 'Backup finished', done: false },
        ],
      },
      'https://radar.example/c/db/cnpg/clusters/db/pg?ctx=kind-db',
    )
    expect(text).toContain('Context: kind-db')
    expect(text).toContain('Cluster UID: uid')
    expect(text).toContain('[unknown] Verify uploads')
    expect(text).toContain('[pending] Backup finished')
    expect(text).toContain('not a shared operation history')
    expect(text).toContain('/c/db/cnpg/clusters/db/pg?ctx=kind-db')
    expect(text).not.toContain('must not be copied')
    expect(text).toContain('not yet checked')
  })
})
