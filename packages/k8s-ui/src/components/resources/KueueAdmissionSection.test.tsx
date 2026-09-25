// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { KueueAdmissionSection } from './KueueAdmissionSection'
import type { KueueAdmissionResponse, SchedulingObservation } from '../../types/scheduling'

const ref = { kind: 'Workload', group: 'kueue.x-k8s.io', namespace: 'ml', name: 'training-abc' }
function response(observation: Partial<SchedulingObservation> = {}): KueueAdmissionResponse {
  return { uid: 'root-uid', installed: true, total: 1, truncated: false, workloads: [{
    apiVersion: 'kueue.x-k8s.io/v1beta2', name: ref.name, namespace: 'ml', uid: 'uid', generation: 2, createdAt: null, deleting: false, ref, projection: 'available', linksLimited: false,
    scheduling: { observations: [{ source: 'kueue', domain: 'admission', subject: ref, subjectGeneration: 2, decision: 'unsatisfied', kueue: { phase: 'pending' }, ...observation }] },
  }] }
}
function render(data?: KueueAdmissionResponse, props: Partial<React.ComponentProps<typeof KueueAdmissionSection>> = {}) {
  return renderToStaticMarkup(<KueueAdmissionSection data={data} loading={false} hinted={false} externalExecution={false} onNavigate={() => {}} {...props} />)
}

describe('Kueue admission investigation', () => {
  it('hides confirmed empty unhinted roots but preserves unavailable and in-flight evidence', () => {
    const empty = { uid: 'root-uid', installed: true, total: 0, truncated: false, workloads: [] }
    expect(render(empty)).toBe('')
    expect(render(empty, { hinted: true })).toContain('No controller-owned Kueue Workload observed')
    expect(render(undefined, { loading: true })).toContain('Looking for controller-owned Workloads')
    expect(render(empty, { error: 'Forbidden', onRetry: () => {} })).toContain('Admission evidence unavailable')
    expect(render(empty, { error: 'Cache not ready' })).not.toContain('No controller-owned')
    expect(render(empty, { error: 'Workload list denied', forbidden: true, onRetry: () => {} })).not.toContain('Retry admission lookup')
    expect(render({ ...empty, installed: false }, { hinted: true })).toContain('not served by this cluster')
  })

  it('does not claim that local absence proves remote execution failed', () => {
    expect(render({ uid: 'root-uid', installed: true, total: 0, truncated: false, workloads: [] }, { hinted: true, externalExecution: true })).toContain('local absence does not establish remote admission or execution state')
  })

  it('shows reported queue reasons and stale generation without inferring causality', () => {
    const html = render(response({ primaryCondition: { type: 'QuotaReserved', status: 'False', reason: 'Inadmissible', message: 'ClusterQueue training is inactive', observedGeneration: 1 }, queues: [{ name: 'ready', roles: ['submission'] }] }))
    expect(html).toContain('Inadmissible')
    expect(html).toContain('ClusterQueue training is inactive')
    expect(html).toContain('Stale evidence')
    expect(html).toContain('submission queue</span>: ready')
    expect(html).not.toContain('suspended because')
  })

  it.each([
    ['pending', 'Pending admission'], ['quota_reserved', 'Quota reserved'], ['admitted', 'Admitted'], ['finished', 'Finished'],
  ] as const)('preserves native phase %s', (phase, label) => {
    const html = render(response({ decision: 'satisfied', kueue: { phase } }))
    expect(html).toContain(label)
    expect(html.includes('Admission status; execution is shown separately below.')).toBe(phase === 'admitted' || phase === 'quota_reserved')
  })

  it('keeps disruptions, inactive state, check retries and requeues distinct', () => {
    const html = render(response({ decision: 'held', kueue: { phase: 'pending', active: false, requeueState: { count: 0, requeueAt: '2026-09-22T00:00:00Z' } }, disruptions: [{ type: 'Evicted', status: 'True', reason: 'Preempted' }], gates: [{ kind: 'preemption_gate', name: 'gate', decision: 'unsatisfied', retryCount: 0, requeueAfterSeconds: 0 }] }))
    expect(html).toContain('Workload inactive')
    expect(html).toContain('Preempted')
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

  it('does not present an unrecognized finish reason as a successful outcome', () => {
    const html = render(response({ decision: 'unknown', primaryCondition: { type: 'Finished', status: 'True', reason: 'CustomFinish' }, kueue: { phase: 'finished' } }))
    expect(html).toContain('CustomFinish')
    expect(html).toContain('Admission unknown')
    expect(html).not.toContain('lucide-check')
  })

  it('shows a deactivation condition once when it is also the primary evidence', () => {
    const condition = { type: 'DeactivationTarget', status: 'True', reason: 'AdmissionCheck' }
    const html = render(response({ decision: 'held', primaryCondition: condition, disruptions: [condition], kueue: { phase: 'pending', active: false } }))
    expect(html).toContain('DeactivationTarget=True')
    expect(html.match(/AdmissionCheck/g)).toHaveLength(1)
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


it('keeps actionable evidence visible while nominal evidence is inert until expanded', async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true
  const container = document.createElement('div')
  const root = createRoot(container)
  const data = response({
    primaryCondition: { type: 'Admitted', status: 'True', reason: 'Admitted', message: 'Primary message' },
    kueue: { phase: 'admitted', podsReady: { type: 'PodsReady', status: 'False', message: 'Workers not ready' }, waitingForReplacementPods: { type: 'WaitingForReplacementPods', status: 'False', message: 'No replacements needed' } },
    gates: [
      { kind: 'admission_check', name: 'ready-check', nativeState: 'Ready', decision: 'satisfied', message: 'Policy accepted' },
      { kind: 'admission_check', name: 'reject-check', nativeState: 'Rejected', decision: 'unsatisfied', message: 'Policy limit exceeded', retryCount: 0 },
    ],
  })
  try {
    await act(async () => root.render(<KueueAdmissionSection data={data} loading={false} hinted externalExecution={false} />))
    const text = (value: string) => [...container.querySelectorAll('p')].find((element) => element.textContent === value)!
    expect(text('Workers not ready').closest('[inert]')).toBeNull()
    expect(text('Policy limit exceeded').closest('[inert]')).toBeNull()
    expect(text('No replacements needed').closest('[inert]')).not.toBeNull()
    expect(text('Policy accepted').closest('[inert]')).not.toBeNull()
    const toggle = [...container.querySelectorAll('button')].find((button) => button.textContent?.startsWith('Technical details'))!
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(toggle.textContent).toContain('1 additional condition')
    expect(toggle.textContent).toContain('1 ready check')
    await act(async () => toggle.click())
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(text('Policy accepted').closest('[inert]')).toBeNull()
    expect(container.textContent?.match(/Primary message/g)).toHaveLength(1)
  } finally { await act(async () => root.unmount()) }
})

it.each([
  { type: 'PodsReady', status: 'True', observedGeneration: 1 },
  { type: 'PodsReady', status: 'Unknown' },
  { type: 'WaitingForReplacementPods', status: 'True' },
])('does not hide stale or non-nominal supporting conditions: %j', async (condition) => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true
  const container = document.createElement('div')
  const root = createRoot(container)
  try {
    await act(async () => root.render(<KueueAdmissionSection data={response({ kueue: { phase: 'pending', [condition.type === 'PodsReady' ? 'podsReady' : 'waitingForReplacementPods']: { ...condition, message: 'Needs attention' } } })} loading={false} hinted externalExecution={false} />))
    const message = [...container.querySelectorAll('p')].find((element) => element.textContent === 'Needs attention')!
    expect(message.closest('[inert]')).toBeNull()
  } finally { await act(async () => root.unmount()) }
})

it.each(['failed', undefined] as const)('never colors a finished %s outcome as successful admission', (outcome) => {
  const html = render(response({ decision: 'satisfied', primaryCondition: { type: 'Finished', status: 'True' }, kueue: { phase: 'finished', outcome, podsReady: { type: 'PodsReady', status: 'False', message: 'No running pods' } } }))
  expect(html).not.toContain('bg-emerald')
  expect(html).toContain('Reported readiness')
  expect(html).toContain('No running pods')
})


it('uses an expanded Section in drawers and keeps the default fullscreen card', async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true
  const container = document.createElement('div')
  const root = createRoot(container)
  try {
    await act(async () => root.render(<KueueAdmissionSection presentation="drawer" data={response()} loading={false} hinted externalExecution={false} />))
    const toggle = [...container.querySelectorAll('button')].find((button) => button.textContent === 'Kueue admission')!
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(container.querySelector('section')).toBeNull()
    await act(async () => toggle.click())
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(container.querySelector('article')!.closest('[inert]')).not.toBeNull()
    await act(async () => root.render(<KueueAdmissionSection data={response()} loading={false} hinted externalExecution={false} />))
    expect(container.querySelector('section[aria-label="Kueue admission"]')).not.toBeNull()
  } finally { await act(async () => root.unmount()) }
})
