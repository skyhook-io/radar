import { describe, expect, it } from 'vitest'
import type { CNPGOperatorResponse, CNPGOperatorVerdict } from '../../api/cnpg'
import { cnpgOperatorActionNote, cnpgOperatorBannerModel, cnpgOperatorConcerns, cnpgOperatorState, cnpgRestartHistory } from './operatorStatus'

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
    // Ready at this read is not "ready" for a component that just restarted.
    expect(cnpgOperatorState(old, now).confirmed).toContain('every component is ready')
    expect(cnpgOperatorState(fresh, now).confirmed).toContain('every component reports ready right now (see the restarts above)')
  })
  it('says nothing about restarts that never happened, and leads with readiness', () => {
    expect(cnpgRestartHistory({ pods: [{ name: 'm-1', ready: true, restarts: 0 }] }, now)).toBeNull()
    const notReady = op([{ name: 'm-1', ready: false, restarts: 0 }])
    notReady.components[0].readyReplicas = 0
    expect(cnpgOperatorConcerns(notReady, now)).toEqual([{ tone: 'unhealthy', text: 'The operator has 0 of 1 replicas ready.' }])
  })
})

describe('operator current state claims only what it read', () => {
  const diag = (webhooks: any[]) => ({
    namespace: 'cnpg-system',
    deployment: 'cnpg-controller-manager',
    pods: [],
    podCoverage: { state: 'ok' },
    leader: { state: 'ok', holderIsCurrentPod: true, stale: false },
    watch: { all: true, namespaces: [], source: '' },
    webhooks,
    webhookServices: [{ state: 'ok', namespace: 'cnpg-system', name: 'cnpg-webhook-service', readyEndpoints: 1, notReadyEndpoints: 0 }],
    metricsPort: 8080,
    reconcile: [],
    events: { state: 'ok', items: [] },
  })
  const op = (webhooks: any[]): CNPGOperatorResponse =>
    ({
      coverage: { deployments: { state: 'full' }, services: { state: 'full' } },
      components: [{ role: 'operator', namespace: 'cnpg-system', deployment: 'cnpg-controller-manager', readyReplicas: 1, replicas: 1, pods: [] }],
      config: [],
      diagnosis: [diag(webhooks)],
    }) as unknown as CNPGOperatorResponse
  const mutating = { state: 'ok', kind: 'MutatingWebhookConfiguration', name: 'cnpg-mutating', webhooks: [{ name: 'm', failurePolicy: 'Fail', caBundleSet: true, service: 'cnpg-system/cnpg-webhook-service', url: false }] }
  it('confirms webhooks only when every configuration was read', () => {
    expect(cnpgOperatorState(op([mutating])).confirmed).toContain('its webhooks have ready endpoints')
    const partial = cnpgOperatorState(op([mutating, { state: 'denied', kind: 'ValidatingWebhookConfiguration', name: 'cnpg-validating', webhooks: [] }]))
    expect(partial.confirmed).not.toContain('its webhooks have ready endpoints')
    expect(partial.unread.join(' ')).toContain('cnpg-validating')
  })
  it('treats an operator scaled to zero as not reconciling', () => {
    const scaled = op([mutating])
    scaled.components[0].readyReplicas = 0
    scaled.components[0].replicas = 0
    expect(cnpgOperatorState(scaled).concerns[0]).toEqual({ tone: 'unhealthy', text: 'The operator is scaled to 0: nothing reconciles.' })
  })
})

describe('clusters blocked on a plugin', () => {
  it('joins the cluster’s plugin phase to that plugin’s Deployment, side by side', () => {
    const op = {
      coverage: { deployments: { state: 'full' }, services: { state: 'full' } },
      components: [{ role: 'plugin', pluginName: 'barman-cloud.cloudnative-pg.io', namespace: 'cnpg-system', deployment: 'barman-cloud', readyReplicas: 1, replicas: 1, pods: [{ name: 'b-1', ready: true, restarts: 0 }] }],
      config: [],
    } as unknown as CNPGOperatorResponse
    const row = {
      namespace: 'pgrt',
      name: 'pg-runtime',
      cluster: { status: { phase: 'Cluster cannot proceed to reconciliation due to an unknown plugin being required' }, spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io' }, { name: 'missing.example.io' }] } },
    } as any
    const out = cnpgOperatorState(op, Date.now(), [row]).concerns
    expect(out[0]).toEqual({
      tone: 'unhealthy',
      text: 'Cluster pgrt/pg-runtime requires a plugin the operator does not know — barman-cloud.cloudnative-pg.io: 1/1 ready; missing.example.io: no Deployment serving it was found.',
    })
  })
})
