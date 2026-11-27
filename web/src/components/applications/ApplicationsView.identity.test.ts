import { describe, expect, it } from 'vitest'

import type { AppRow, AppWorkload, Issue } from '@skyhook-io/k8s-ui'
import { appIssuesForApplication } from './ApplicationsView'

function workload(group: string): AppWorkload {
  return { kind: 'Job', group, namespace: 'ml', name: 'train', health: 'healthy', ready: 1, desired: 1, restarts: 0 }
}

function issue(id: string, group?: string): Issue {
  return {
    id,
    severity: 'warning',
    source: 'condition',
    category: 'job_failed',
    category_group: 'runtime',
    grouping_scope: 'workload',
    kind: 'Job',
    group,
    namespace: 'ml',
    name: 'train',
    reason: id,
    message: id,
  }
}

function application(key: string, workloads: AppWorkload[]): AppRow {
  return { key, name: key, workloads } as AppRow
}

describe('application issue membership', () => {
  it('does not share an issue between same-named core and Volcano Jobs', () => {
    const issues = [issue('core', 'batch'), issue('volcano', 'batch.volcano.sh')]
    const core = application('core', [workload('batch')])
    const volcano = application('volcano', [workload('batch.volcano.sh')])

    expect(appIssuesForApplication(issues, core, [core, volcano], true).map(item => item.id)).toEqual(['core'])
    expect(appIssuesForApplication(issues, volcano, [core, volcano], true).map(item => item.id)).toEqual(['volcano'])
  })

  it('treats an omitted built-in group as the canonical built-in identity', () => {
    const core = application('core', [workload('batch')])
    const volcano = application('volcano', [workload('batch.volcano.sh')])

    expect(appIssuesForApplication([issue('legacy')], core, [core, volcano], true).map(item => item.id)).toEqual(['legacy'])
    expect(appIssuesForApplication([issue('legacy')], volcano, [core, volcano], true)).toEqual([])
  })

  it('includes an issue on an owned Service alongside affected workloads, without duplicates', () => {
    const app = application('checkout', [workload('batch')])
    const service = { kind: 'Service', group: '', namespace: 'ml', name: 'checkout' }
    app.relationships = { serviceRefs: [service] }
    const endpoints = { ...issue('no-endpoints'), ...service, grouping_scope: 'service' }
    const running = issue('workload', 'batch')
    const affected = { ...issue('affected'), kind: 'Pod', name: 'pod-123', owner: workload('batch') }
    const issues = [endpoints, running, running, affected]

    expect(appIssuesForApplication(issues, app, [app], true).map(item => item.id))
      .toEqual(['no-endpoints', 'workload', 'affected'])
    expect(appIssuesForApplication(issues, app, [app], false).map(item => item.id))
      .toEqual(['workload', 'affected'])

    const other = { ...application('worker', []), relationships: { serviceRefs: [service] } }
    expect(appIssuesForApplication(issues, app, [app, other], true).map(item => item.id))
      .toEqual(['workload', 'affected'])
  })
})
