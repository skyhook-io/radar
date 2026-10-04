import { describe, expect, it } from 'vitest'
import type { CNPGOperatorResponse, CNPGOperatorVerdict } from '../../api/cnpg'
import { cnpgOperatorActionNote, cnpgOperatorBannerModel, cnpgOperatorConcerns, cnpgRestartHistory } from './operatorStatus'

const down: CNPGOperatorVerdict = {
  state: 'notReconciling',
  reasons: ['no operator instance has renewed the leader lease cnpg-system/db9c8771.cnpg.io'],
  webhookRejects: true,
  webhookReason: 'the admission webhook Service cnpg-system/cnpg-webhook-service has no ready endpoint and fails closed, so the API server rejects writes to CloudNativePG objects',
  operator: 'cnpg-system/cnpg-controller-manager',
}
const up: CNPGOperatorVerdict = { state: 'reconciling', webhookRejects: false }
const unknown: CNPGOperatorVerdict = { state: 'unknown', unknown: 'the leader lease is not readable (needs get leases in cnpg-system)', webhookRejects: null }

describe('cnpgOperatorBannerModel', () => {
  it('names the namespaces whose operator is down and why', () => {
    const m = cnpgOperatorBannerModel({ pg: down, other: up }, ['pg', 'other', 'pg'])
    expect(m?.stale?.namespaces).toEqual(['pg'])
    expect(m?.stale?.reasons[0]).toMatch(/^No operator instance has renewed the leader lease/)
    expect(m?.rejects).toMatch(/no ready endpoint/)
  })
  it('stays quiet when reconciling or unknown', () => {
    expect(cnpgOperatorBannerModel({ pg: up }, ['pg'])).toBeNull()
    expect(cnpgOperatorBannerModel({ pg: unknown }, ['pg'])).toBeNull()
    expect(cnpgOperatorBannerModel(undefined, ['pg'])).toBeNull()
  })
})

describe('cnpgOperatorActionNote', () => {
  it('warns when the operator is observed down and notes when unknown', () => {
    expect(cnpgOperatorActionNote(down)?.tone).toBe('warning')
    expect(cnpgOperatorActionNote(unknown)).toEqual({ tone: 'info', text: expect.stringContaining('needs get leases') })
    expect(cnpgOperatorActionNote(up)).toBeNull()
  })
})

describe('cnpgOperatorActionNote when unknown', () => {
  it('says briefly what could not be confirmed', () => {
    expect(cnpgOperatorActionNote({ state: 'unknown', unknown: "couldn't read its leader lease" } as never)).toEqual({
      tone: 'info',
      text: "Radar couldn't confirm the operator is running (couldn't read its leader lease).",
    })
  })
})

describe('operator current state', () => {
  const now = Date.parse('2026-10-04T10:00:00Z')
  const op = (pods: NonNullable<CNPGOperatorResponse['components'][number]['pods']>): CNPGOperatorResponse => ({
    coverage: { deployments: { state: 'full' }, services: { state: 'full' } },
    components: [{ role: 'operator', namespace: 'cnpg-system', deployment: 'cnpg-controller-manager', readyReplicas: 1, replicas: 1, pods }],
    config: [],
  })
  it('words restart history with when the last one ended, and only a recent one as trouble', () => {
    const old = op([{ name: 'm-1', ready: true, restarts: 33, lastTermination: { container: 'manager', reason: 'Error', exitCode: 1, finishedAt: '2026-10-04T07:53:31Z' } }])
    expect(cnpgRestartHistory(old.components[0], now)).toMatchObject({ recent: false })
    expect(cnpgRestartHistory(old.components[0], now)!.text).toMatch(/^33 restarts since the Pod was created · last ended .* ago \(Error, exit 1\)$/)
    expect(cnpgOperatorConcerns(old, now)).toEqual([expect.objectContaining({ tone: 'neutral' })])
    const fresh = op([{ name: 'm-1', ready: true, restarts: 2, lastTermination: { container: 'manager', reason: 'OOMKilled', exitCode: 137, finishedAt: '2026-10-04T09:40:00Z' } }])
    expect(cnpgOperatorConcerns(fresh, now)[0]).toMatchObject({ tone: 'degraded' })
  })
  it('says nothing about restarts that never happened, and leads with readiness', () => {
    expect(cnpgRestartHistory({ pods: [{ name: 'm-1', ready: true, restarts: 0 }] }, now)).toBeNull()
    const notReady = op([{ name: 'm-1', ready: false, restarts: 0 }])
    notReady.components[0].readyReplicas = 0
    expect(cnpgOperatorConcerns(notReady, now)).toEqual([{ tone: 'unhealthy', text: 'The operator has 0 of 1 replicas ready.' }])
  })
})
