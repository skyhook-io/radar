import { describe, expect, it } from 'vitest'

import type { AppWorkload, Issue } from '@skyhook-io/k8s-ui'
import { appIssuesForWorkloads } from './ApplicationsView'

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

describe('appIssuesForWorkloads identity', () => {
  it('does not share an issue between same-named core and Volcano Jobs', () => {
    const issues = [issue('core', 'batch'), issue('volcano', 'batch.volcano.sh')]

    expect(appIssuesForWorkloads(issues, [workload('batch')]).map(item => item.id)).toEqual(['core'])
    expect(appIssuesForWorkloads(issues, [workload('batch.volcano.sh')]).map(item => item.id)).toEqual(['volcano'])
  })

  it('treats an omitted built-in group as the canonical built-in identity', () => {
    expect(appIssuesForWorkloads([issue('legacy')], [workload('batch')]).map(item => item.id)).toEqual(['legacy'])
    expect(appIssuesForWorkloads([issue('legacy')], [workload('batch.volcano.sh')])).toEqual([])
  })
})
