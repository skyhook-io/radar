import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { JobSetMemberComparison } from './JobSetMemberComparison'
import type { JobSetUsage } from '../../api/client'

const empty: JobSetUsage = { runningPods: 0, reportingPods: 0, stalePods: 0, cpu: null, memory: null, cpuRequest: 0, memoryRequest: 0 }
function render(usage: JobSetUsage) {
  return renderToStaticMarkup(<JobSetMemberComparison runs={[{ group: 'batch', kind: 'jobs', namespace: 'ns', name: 'leader', phase: 'Running', active: true, jobset: { replicatedJob: 'leader', jobIndex: '0' } }]} total={250} filteredTotal={1} truncated={false} loading={false} error={null} roles={['leader']} role="leader" onRoleChange={() => {}} search="" onSearchChange={() => {}} state="all" onStateChange={() => {}} selected="jobs/ns/leader" resourcesLoading={false} resourceError={null} durationFor={() => '2m'} resources={{ uid: 'root', source: 'metrics.k8s.io', total: usage, members: { leader: usage } }} />)
}

describe('JobSet resource comparison', () => {
  it('keeps absent usage distinct from measured zero and completed Pods', () => {
    const absent = render({ ...empty, runningPods: 2 })
    expect(absent).toContain('0/2 running Pods reporting')
    expect(absent).not.toContain('0m')
    const zero = render({ ...empty, runningPods: 1, reportingPods: 1, cpu: 0, memory: 0 })
    expect(zero).toContain('1/1 running Pods reporting')
    expect(zero).not.toContain('Unavailable')
    expect(render(empty)).toContain('No running Pods')
  })
  it('shows partial coverage, stale samples, independent counts and declared extended requests', () => {
    const html = render({ ...empty, runningPods: 2, reportingPods: 1, stalePods: 1, cpu: 25000000, memory: 33554432, cpuRequest: 100000000, memoryRequest: 67108864, extendedRequests: { 'nvidia.com/gpu': '2' } })
    expect(html).toContain('1/2 running Pods reporting')
    expect(html).toContain('1 stale')
    expect(html).toContain('250 retained')
    expect(html).toContain('1 matching')
    expect(html).toContain('usage not collected')
    expect(html).toContain('outside these filters')
  })
})
