import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ClusterQueueRenderer, LocalQueueRenderer } from './KueueQueueRenderers'
import { getAdmissionCheckStatus, getClusterQueueStatus, getLocalQueueStatus, getKueueQueueQuotaRows } from '../resource-utils-kueue'

const base = { apiVersion: 'kueue.x-k8s.io/v1beta2', metadata: { generation: 3 }, spec: {} }
const render = (data: any, local = false) => renderToStaticMarkup(local ? <LocalQueueRenderer data={data} /> : <ClusterQueueRenderer data={data} />)
const usage = (name: string, resources: Record<string, string>) => ({ name, resources: Object.entries(resources).map(([name, total]) => ({ name, total })) })

describe('queue quota accounting', () => {
  it('joins by flavor and resource, preserves declared order, and retains status-only entries', () => {
    const data = { ...base, spec: { resourceGroups: [{ flavors: [
      { name: 'b', resources: [{ name: 'cpu', nominalQuota: '500m', borrowingLimit: 0 }, { name: 'memory', nominalQuota: '2Gi' }] },
      { name: 'a', resources: [{ name: 'nvidia.com/gpu', nominalQuota: '0', lendingLimit: '0' }] },
    ] }] }, status: {
      flavorsReservation: [usage('a', { 'nvidia.com/gpu': '1' }), usage('b', { memory: '1Gi', cpu: '0' }), usage('old', { cpu: '2' })],
      flavorsUsage: [usage('b', { cpu: '0' })],
    } }
    expect(getKueueQueueQuotaRows(data, true)).toEqual([
      { flavor: 'b', resource: 'cpu', configured: true, nominal: '500m', borrowingLimit: 0, reserved: '0', used: '0' },
      { flavor: 'b', resource: 'memory', configured: true, nominal: '2Gi', reserved: '1Gi' },
      { flavor: 'a', resource: 'nvidia.com/gpu', configured: true, nominal: '0', lendingLimit: '0', reserved: '1' },
      { flavor: 'old', resource: 'cpu', configured: false, reserved: '2' },
    ])
    expect(render(data)).toContain('Not in current configuration')
    expect(render(data)).toContain('Not reported')
  })

  it('keeps borrowed amounts inside totals and distinguishes zero limits from uncapped borrowing', () => {
    const data = { ...base, spec: { cohortName: 'shared', resourceGroups: [{ flavors: [{ name: 'cpu', resources: [
      { name: 'cpu', nominalQuota: '1', borrowingLimit: '0', lendingLimit: 0 }, { name: 'memory', nominalQuota: '1Gi' },
    ] }] }] }, status: { flavorsReservation: [{ name: 'cpu', resources: [{ name: 'cpu', total: '2', borrowed: '1' }] }] } }
    const html = render(data)
    for (const text of ['Borrowing limit: 0', 'Lending limit: 0', 'borrowing has no configured cap', 'all nominal quota may be lent', 'Borrowed reservation: 1']) expect(html).toContain(text)
    expect(getKueueQueueQuotaRows(data, true)[0].reserved).toBe('2')
    expect(render({ ...data, spec: { ...data.spec, cohortName: undefined } })).not.toContain('Borrowing limit:')
  })

  it('reads the actual v1beta1 LocalQueue usage spelling without importing parent quota', () => {
    const data = { ...base, apiVersion: 'kueue.x-k8s.io/v1beta1', spec: { clusterQueue: 'shared' }, status: { flavorUsage: [usage('cpu', { cpu: '50m' })] } }
    expect(getKueueQueueQuotaRows(data, false)[0].used).toBe('50m')
    expect(render(data, true)).toContain('50m')
    expect(render(data, true)).not.toContain('Nominal')
    expect(getKueueQueueQuotaRows({ ...data, apiVersion: base.apiVersion }, false)).toEqual([])
    expect(getKueueQueueQuotaRows({ ...data, apiVersion: base.apiVersion, status: { flavorsUsage: data.status.flavorUsage } }, false)[0].used).toBe('50m')
  })
})

describe('queue status and policy', () => {
  it('does not turn absent status into zero workloads or healthy state', () => {
    const html = render(base)
    expect(html).toContain('Controller status has not been reported')
    expect(html).not.toContain('No Active condition reported')
    expect(html).toContain('Unknown')
    const counted = render({ ...base, status: { pendingWorkloads: 0, reservingWorkloads: 1, admittedWorkloads: 1 } })
    expect(counted).not.toContain('counts have not been reported')
    expect(counted).toContain('not additive')
  })

  it('keeps observed stops neutral, unrelated AdmissionChecks unchanged, and stale evidence unknown', () => {
    const stopped = { ...base, status: { conditions: [{ type: 'Active', status: 'False', reason: 'Stopped', observedGeneration: 3 }] } }
    expect(getClusterQueueStatus(stopped).level).toBe('neutral')
    expect(getLocalQueueStatus(stopped).level).toBe('neutral')
    expect(getAdmissionCheckStatus(stopped).level).toBe('alert')
    expect(render(stopped)).not.toContain('failing')
    const stale = { ...stopped, metadata: { generation: 4 } }
    expect(getClusterQueueStatus(stale).level).toBe('unknown')
    expect(render(stale)).toContain('earlier generation')
  })

  it('does not claim a parent is held based on LocalQueue inactivity', () => {
    const data = { ...base, spec: { clusterQueue: 'parent' }, status: { conditions: [{ type: 'Active', status: 'False', reason: 'ClusterQueueIsInactive', message: 'parent inactive' }] } }
    expect(getLocalQueueStatus(data).level).toBe('alert')
    expect(render(data, true)).toContain('Inspect the ClusterQueue')
    expect(render(data, true)).not.toContain('Configured to stop')
  })

  it.each(['Hold', 'HoldAndDrain'])('separates requested %s from an observed failure and explains reservations', (stopPolicy) => {
    const data = { ...base, spec: { stopPolicy }, status: { conditions: [{ type: 'Active', status: 'False', reason: 'FlavorNotFound', message: 'flavor missing' }] } }
    expect(getClusterQueueStatus(data).text).toBe('FlavorNotFound')
    const html = render(data)
    expect(html).toContain('cancel reservations')
    expect(html).toContain(stopPolicy === 'Hold' ? 'may continue' : 'are to be evicted')
    expect(html).toContain('flavor missing')
  })

  it('distinguishes no namespaces eligible from all and from a selector', () => {
    expect(render(base)).toContain('No namespaces eligible')
    expect(render({ ...base, spec: { namespaceSelector: {} } })).toContain('All namespaces')
    expect(render({ ...base, spec: { namespaceSelector: { matchLabels: { team: 'ml' } } } })).toContain('team=ml')
  })

  it('preserves v1beta1 cohort and both check declarations with flavor applicability', () => {
    const html = render({ ...base, apiVersion: 'kueue.x-k8s.io/v1beta1', spec: {
      cohort: 'legacy-cohort', admissionChecks: ['all-check'],
      admissionChecksStrategy: { admissionChecks: [{ name: 'gpu-check', onFlavors: ['a100'] }] },
    } })
    for (const text of ['legacy-cohort', 'all-check', 'gpu-check', 'Applies to all flavors', 'Applies to flavors: a100']) expect(html).toContain(text)
  })
})


it('omits zero borrowing and irrelevant cohort policies on a real-shaped standalone queue', () => {
  const data = { ...base, spec: {
    resourceGroups: [{ flavors: [{ name: 'cpu', resources: [{ name: 'cpu', nominalQuota: '2' }] }] }],
    preemption: { reclaimWithinCohort: 'Never', borrowWithinCohort: { policy: 'Never' } },
    flavorFungibility: { whenCanBorrow: 'MayStopSearch' },
  }, status: {
    flavorsReservation: [{ name: 'cpu', resources: [{ name: 'cpu', total: '0', borrowed: '0' }] }],
    flavorsUsage: [{ name: 'cpu', resources: [{ name: 'cpu', total: '0', borrowed: '0' }] }],
  } }
  const html = render(data)
  expect(html).not.toContain('Nominal quota is not a hard ceiling')
  for (const text of ['Borrowed reservation:', 'Reclaim within Cohort', 'Borrow with Preemption', 'Flavor when Borrowing']) expect(html).not.toContain(text)
  const cohort = render({ ...data, spec: { ...data.spec, cohortName: 'shared' } })
  expect(cohort).toContain('Nominal quota is not a hard ceiling')
  expect(cohort).toContain('borrowing has no configured cap')
  expect(cohort).not.toContain('Borrowing limit:')
})

it('uses the Active label consistently for stale healthy evidence', () => {
  expect(getClusterQueueStatus({ ...base, status: { conditions: [{ type: 'Active', status: 'True', reason: 'Ready', observedGeneration: 2 }] } }).text).toBe('Active (stale)')
})
