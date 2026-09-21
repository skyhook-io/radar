import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { KueueAdmissionSection } from './KueueAdmissionSection'
import type { KueueAdmissionResponse, SchedulingObservation } from '../../types/scheduling'

const ref = { kind: 'Workload', group: 'kueue.x-k8s.io', namespace: 'ml', name: 'training-abc' }
function response(observation: Partial<SchedulingObservation> = {}): KueueAdmissionResponse {
  return { installed: true, total: 1, truncated: false, workloads: [{
    apiVersion: 'kueue.x-k8s.io/v1beta2', name: ref.name, namespace: 'ml', uid: 'uid', generation: 2, createdAt: null, deleting: false, ref, projection: 'available',
    scheduling: { observations: [{ source: 'kueue', domain: 'admission', subject: ref, subjectGeneration: 2, decision: 'unsatisfied', kueue: { phase: 'pending' }, ...observation }] },
  }] }
}
function render(data?: KueueAdmissionResponse, props: Partial<React.ComponentProps<typeof KueueAdmissionSection>> = {}) {
  return renderToStaticMarkup(<KueueAdmissionSection data={data} loading={false} hinted={false} externalExecution={false} onNavigate={() => {}} {...props} />)
}

describe('Kueue admission investigation', () => {
  it('hides confirmed empty unhinted roots but preserves unavailable and in-flight evidence', () => {
    const empty = { installed: true, total: 0, truncated: false, workloads: [] }
    expect(render(empty)).toBe('')
    expect(render(empty, { hinted: true })).toContain('No controller-owned Kueue Workload observed')
    expect(render(undefined, { loading: true })).toContain('Looking for controller-owned Workloads')
    expect(render(empty, { error: 'Forbidden', onRetry: () => {} })).toContain('Admission evidence unavailable: Forbidden')
    expect(render(empty, { error: 'Cache not ready' })).not.toContain('No controller-owned')
    expect(render({ ...empty, installed: false }, { hinted: true })).toContain('not served by this cluster')
  })

  it('does not claim that local absence proves remote execution failed', () => {
    expect(render({ installed: true, total: 0, truncated: false, workloads: [] }, { hinted: true, externalExecution: true })).toContain('local absence does not establish remote admission or execution state')
  })

  it('shows reported queue reasons and stale generation without inferring causality', () => {
    const html = render(response({ primaryCondition: { type: 'QuotaReserved', status: 'False', reason: 'Inadmissible', message: 'ClusterQueue training is inactive', observedGeneration: 1 }, queues: [{ name: 'ready', roles: ['submission'] }] }))
    expect(html).toContain('Inadmissible')
    expect(html).toContain('ClusterQueue training is inactive')
    expect(html).toContain('stale evidence')
    expect(html).toContain('submission queue: ready')
    expect(html).not.toContain('suspended because')
  })

  it.each([
    ['pending', 'Pending admission'], ['quota_reserved', 'Quota reserved'], ['admitted', 'Admitted'], ['finished', 'Finished'],
  ] as const)('preserves native phase %s', (phase, label) => {
    const html = render(response({ decision: 'satisfied', kueue: { phase } }))
    expect(html).toContain(label)
    expect(html).toContain('does not prove that Jobs or Pods are running')
  })

  it('keeps disruptions, inactive state, check retries and requeues distinct', () => {
    const html = render(response({ decision: 'held', kueue: { phase: 'pending', active: false, requeueState: { count: 0, requeueAt: '2026-09-22T00:00:00Z' } }, disruptions: [{ type: 'Evicted', status: 'True', reason: 'Preempted' }], gates: [{ kind: 'preemption_gate', name: 'gate', decision: 'unsatisfied', retryCount: 0, requeueAfterSeconds: 0 }] }))
    expect(html).toContain('Workload inactive')
    expect(html).toContain('Evicted=True')
    expect(html).toContain('alone does not establish an admission blocker')
    expect(html).toContain('Retries: 0')
    expect(html).toContain('Requeues: 0')
  })

  it('does not turn unsupported or unreadable Workloads into an absent association', () => {
    const data = response()
    data.workloads[0].projection = 'unsupported'
    data.workloads[0].apiVersion = 'kueue.x-k8s.io/v1beta1'
    expect(render(data)).toContain('Scheduling projection is unavailable for kueue.x-k8s.io/v1beta1')
    data.workloads[0].projection = 'forbidden'
    data.workloads[0].ref = undefined
    const html = render(data)
    expect(html).toContain('permission to get this Workload is required')
    expect(html).not.toContain('<button')
  })

  it('keeps bounded records separate and never names the newest as current', () => {
    const data = response()
    data.total = 9
    data.truncated = true
    data.workloads.push({ ...data.workloads[0], uid: 'second', name: 'older', deleting: true })
    const html = render(data)
    expect(html).toContain('2 of 9 Workloads shown')
    expect(html).toContain('order does not identify a current attempt')
    expect(html).toContain('Deleting')
  })
})
