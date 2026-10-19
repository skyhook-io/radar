import { describe, expect, it } from 'vitest'
import { topologyViewLimitMessage, topologyWarningNotice } from './inventory-notice'

describe('Topology view-limit notice', () => {
  it('describes loading instead of retained history', () => {
    expect(topologyViewLimitMessage([{ resource: 'pods', state: 'pending', message: 'Pod data is still loading' }])).toBe('Some resource data is still loading.')
  })
  it('describes a namespace limit instead of an outage or retained history', () => {
    expect(topologyViewLimitMessage([{ resource: 'pods', state: 'uncovered', message: 'Pod data may be incomplete' }])).toBe('Radar watches only selected namespaces.')
  })
  it('keeps both independent limits visible', () => {
    expect(topologyViewLimitMessage([{ resource: 'pods', state: 'pending', message: '' }, { resource: 'services', state: 'uncovered', message: '' }])).toBe('Some resource data is still loading; Radar watches only selected namespaces.')
  })
  it('preserves the existing history-only explanation', () => {
    expect(topologyViewLimitMessage(undefined)).toBe('Large retained run history is summarized to keep the graph readable.')
  })
})

describe('Mixed topology warning categories', () => {
  it('keeps RBAC and inventory limits informational when there is no failed source', () => {
    const notice = topologyWarningNotice({warnings: ['Secrets not available (RBAC not granted)', 'Topology view: Pod data is still loading'], incompleteInventories: [{resource: 'pods', state: 'pending', message: 'Pod data is still loading'}]})
    expect(notice.label).toBe('Limited access · view limited:')
    expect(notice.message).toBe('1 resource type not accessible due to RBAC restrictions. Some resource data is still loading.')
    expect(notice.isViewLimited).toBe(true)
    expect(notice.message).not.toContain('failed')
  })
  it('retains the actual failure notice when a source failed', () => {
    const notice = topologyWarningNotice({warnings: ['Secrets not available (RBAC not granted)', 'Topology view: Pod data is still loading', 'Failed to list Services']})
    expect(notice.label).toBe('Warning:')
    expect(notice.message).toBe('Some resources failed to load. Data may be incomplete.')
  })
})
