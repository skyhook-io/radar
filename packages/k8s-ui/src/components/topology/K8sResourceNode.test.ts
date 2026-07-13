import { describe, expect, it } from 'vitest'
import { baseSubtitle } from './K8sResourceNode'

describe('baseSubtitle', () => {
  it('describes workflow templates without dropping newer topology kinds', () => {
    expect(baseSubtitle('WorkflowTemplate', { entrypoint: 'main', templateCount: 3 })).toBe('main • 3 templates')
    expect(baseSubtitle('ClusterWorkflowTemplate', { templateCount: 2 })).toBe('2 templates')
    expect(baseSubtitle('ServiceAccount', {})).toBe('Workload identity')
    expect(baseSubtitle('ServiceMonitor', { endpointCount: 1 })).toBe('1 scrape endpoint')
  })
})
