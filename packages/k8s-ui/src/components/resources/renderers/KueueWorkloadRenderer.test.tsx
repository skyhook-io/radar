import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { KueueWorkloadRenderer, getKueueWorkloadConditionTone } from './KueueWorkloadRenderer'

const base = {
  apiVersion: 'kueue.x-k8s.io/v1beta2', kind: 'Workload',
  metadata: { name: 'training', namespace: 'ml' },
  spec: { queueName: 'gpu', podSets: [{ name: 'workers', count: 4, minCount: 2 }] },
}
const render = (status: any = {}, data: any = base) => renderToString(<KueueWorkloadRenderer data={{ ...data, status }} />).replace(/<!-- -->/g, '')

describe('Kueue Workload detail', () => {
  it('shows evaluated totals and exact quota blocker without inventing a failure', () => {
    const html = render({
      resourceRequests: [{ name: 'workers', resources: { cpu: '8', memory: '32Gi', 'nvidia.com/gpu': '4' } }],
      conditions: [{ type: 'QuotaReserved', status: 'False', reason: 'Pending', message: 'insufficient quota for nvidia.com/gpu' }],
    })
    for (const text of ['Evaluated Requests', 'CPU: 8', 'nvidia.com/gpu: 4', 'insufficient quota', 'No reservation reported']) expect(html).toContain(text)
    expect(html).not.toContain('failing')
    expect(html).not.toContain('Workload finished unsuccessfully')
  })

  it.each(['v1beta1', 'v1beta2'])('separates reservation from admission for %s', (version) => {
    const html = render({
      admission: { clusterQueue: 'team-quota', podSetAssignments: [{ name: 'workers', count: 2, resourceUsage: { cpu: '4', 'nvidia.com/gpu': '2' }, flavors: { 'nvidia.com/gpu': 'a100' } }] },
      admissionChecks: [{ name: 'capacity', state: 'Pending', message: 'Waiting for provisioned nodes' }],
      conditions: [{ type: 'QuotaReserved', status: 'True' }, { type: 'Admitted', status: 'False', reason: 'AdmissionCheck' }],
    }, { ...base, apiVersion: `kueue.x-k8s.io/${version}` })
    for (const text of ['QuotaReserved', 'Pods at Reservation', 'Resources at Reservation', 'CPU: 4', 'nvidia.com/gpu: a100', 'team-quota', 'Waiting for provisioned nodes']) expect(html).toContain(text)
    expect(html).not.toContain('Admitted Pods')
    expect(html).not.toContain('Admitted Demand')
  })

  it('joins PodSet assignments by name rather than array position', () => {
    const html = render({ admission: { podSetAssignments: [{ name: 'other', resourceUsage: { cpu: '99' } }, { name: 'workers', count: 0, resourceUsage: { cpu: 0, 'example.com/gpu': '0' } }] }, reclaimablePods: [{ name: 'workers', count: 0 }] })
    expect(html).toContain('example.com/gpu: 0')
    expect(html).toContain('CPU: 0')
    expect(html).toContain('Reclaimable Pods')
    expect(html).not.toContain('CPU: 99')
  })

  it('does not reconstruct controller totals or turn missing status into zero', () => {
    const html = render({}, { ...base, spec: { podSets: [{ name: 'workers', count: 4, template: { spec: { containers: [{ resources: { requests: { cpu: '12' } } }] } } }] } })
    expect(html).toContain('Not reported')
    expect(html).not.toContain('CPU: 12')
    expect(html).not.toContain('Pods at Reservation')
  })

  it.each(['Failed', 'FailedToStart', 'OutOfSync', 'OwnerNotFound'])('highlights terminal %s with its exact reason', (reason) => {
    const html = render({ conditions: [{ type: 'Finished', status: 'True', reason, message: 'controller explanation' }] })
    expect(html).toContain('Workload finished unsuccessfully')
    expect(html).toContain('controller explanation')
    expect(getKueueWorkloadConditionTone({ type: 'Finished', status: 'True', reason })).toBe('fail')
  })

  it('keeps unknown completion and condition semantics neutral', () => {
    expect(getKueueWorkloadConditionTone({ type: 'Finished', status: 'True', reason: 'FutureReason' })).toBe('unknown')
    expect(getKueueWorkloadConditionTone({ type: 'FutureCondition', status: 'True' })).toBe('unknown')
    expect(getKueueWorkloadConditionTone({ type: 'Admitted', status: 'Unknown' })).toBe('unknown')
  })

  it.each(['Ready', 'Pending', 'Retry', 'Rejected', 'FutureState'])('preserves admission check %s and its message', (state) => {
    const html = render({ admissionChecks: [{ name: 'capacity', state, message: 'Exact controller message' }] })
    expect(html).toContain(state)
    expect(html).toContain('Exact controller message')
  })
})

it('labels stale failure evidence without displaying it as a current failure', () => {
  const html = render({ conditions: [{ type: 'Finished', status: 'True', reason: 'FailedToStart', observedGeneration: 2 }] }, { ...base, metadata: { ...base.metadata, generation: 3 } })
  expect(html).toContain('earlier generation')
  expect(html).toContain('Finished (generation 2)')
  expect(html).not.toContain('Workload finished unsuccessfully')
  expect(html).not.toContain('failing')
})


it('does not neutralize current admission because a historical condition is stale', () => {
  const html = render({ conditions: [{ type: 'Admitted', status: 'True', observedGeneration: 3 }, { type: 'Requeued', status: 'False', observedGeneration: 2 }] }, { ...base, metadata: { ...base.metadata, generation: 3 } })
  expect(html).toContain('Admitted')
  expect(html).not.toContain('earlier generation')
})


it.each([
  [{ priority: 0 }, '0'],
  [{ priorityClassName: 'batch-priority' }, 'batch-priority'],
  [{ priorityClassRef: { name: 'training-priority' } }, 'training-priority'],
])('shows priority from native fields %j', (fields, value) => {
  const html = render({}, { ...base, spec: { ...base.spec, ...fields } })
  expect(html).toContain('Priority')
  expect(html).toContain(value)
})

it('labels a stale terminal report even when an admission condition has a newer generation', () => {
  const html = render({ conditions: [
    { type: 'Finished', status: 'True', reason: 'FailedToStart', observedGeneration: 2 },
    { type: 'Admitted', status: 'True', observedGeneration: 3 },
  ] }, { ...base, metadata: { ...base.metadata, generation: 3 } })
  expect(html).toContain('Reported state describes an earlier generation')
  expect(html).toContain('Reported state may not reflect the current specification')
  expect(html).not.toContain('Workload finished unsuccessfully')
})
