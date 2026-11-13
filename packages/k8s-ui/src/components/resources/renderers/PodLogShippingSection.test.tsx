import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PodLogShippingSection } from './PodLogShippingSection'
import type { LoggingPodShipping, LoggingPodFlow } from '../../../types/core'

const flow = (patch: Partial<LoggingPodFlow>): LoggingPodFlow => ({
  kind: 'ClusterFlow', namespace: 'logging-system', name: 'optin', selected: false, uncertain: false, ignored: false,
  unmet: [], hasFilters: false, outputs: [{ kind: 'ClusterOutput', name: 'loki', type: 'loki', active: true }], ...patch,
})
const answer = (patch: Partial<LoggingPodShipping>): LoggingPodShipping => ({ installed: true, flows: [], shipped: false, uncertain: false, readErrors: [], ...patch })
const render = (data: LoggingPodShipping | null, error: Error | null = null) =>
  renderToString(<PodLogShippingSection data={data} loading={false} error={error} />).replace(/<!-- -->/g, '')

describe('PodLogShippingSection', () => {
  it('lists selecting Flows first with outputs', () => {
    const html = render(answer({ shipped: true, flows: [flow({ selected: true })] }))
    expect(html).toContain('Shipped by')
    expect(html).toContain('optin')
    expect(html).toContain('loki')
  })
  it('warns that a filtering Flow may drop lines', () => {
    expect(render(answer({ shipped: true, flows: [flow({ selected: true, hasFilters: true })] }))).toContain('This Flow filters records; some lines may be dropped.')
  })
  it('names the closest Flow when none selects', () => {
    const html = render(answer({ flows: [
      flow({ name: 'far', unmet: ['label a=1', 'label b=2'] }),
      flow({ name: 'optin', unmet: ['label example.com/loki-logs=true'] }),
    ] }))
    expect(html).toContain('Not shipped: no Flow selects this pod')
    expect(html).toContain('Closest match: optin')
    expect(html).toContain('label example.com/loki-logs=true')
  })
  it('says may be selected when uncertain', () => {
    const html = render(answer({ uncertain: true, flows: [flow({ uncertain: true })] }))
    expect(html).toContain('may be selected')
    expect(html).not.toContain('Not shipped')
  })
  it('never says not shipped when Flows were unread', () => {
    const html = render(answer({ readErrors: ['clusterflows'], flows: [flow({ unmet: ['label x=1'] })] }))
    expect(html).toContain('Radar could not read ClusterFlows')
    expect(html).not.toContain('Not shipped')
  })
  it('renders nothing when not installed', () => {
    expect(render(answer({ installed: false }))).toBe('')
  })
  it('shows a failed request as an alert, not as not shipped', () => {
    const html = render(null, new Error('boom'))
    expect(html).toContain('boom')
    expect(html).not.toContain('Not shipped')
  })
})
