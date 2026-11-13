import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PodLogShippingSection } from './PodLogShippingSection'
import type { LoggingPodShipping, LoggingPodFlow } from '../../../types/core'

const flow = (patch: Partial<LoggingPodFlow>): LoggingPodFlow => ({
  kind: 'ClusterFlow', namespace: 'logging-system', name: 'optin', selected: false, uncertain: false, ignored: false, excluded: false,
  unmet: [], hasFilters: false, outputs: [{ kind: 'ClusterOutput', namespace: 'logging-system', name: 'loki', type: 'loki', found: true, active: true }], ...patch,
})
const answer = (patch: Partial<LoggingPodShipping>): LoggingPodShipping => ({ installed: true, flows: [], shipped: false, uncertain: false, readErrors: [], ...patch })
const render = (data: LoggingPodShipping | null, error: Error | null = null) =>
  renderToString(<PodLogShippingSection data={data} loading={false} error={error} />).replace(/<!-- -->/g, '')

describe('PodLogShippingSection', () => {
  it('lists selecting Flows first with outputs', () => {
    const html = render(answer({ shipped: true, flows: [flow({ selected: true, delivers: true })] }))
    expect(html).toContain('Shipped by')
    expect(html).toContain('optin')
    expect(html).toContain('loki')
  })
  it('warns that a filtering Flow may drop lines', () => {
    expect(render(answer({ shipped: true, flows: [flow({ selected: true, delivers: true, hasFilters: true })] }))).toContain('This Flow filters records; some lines may be dropped.')
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
  it('says a selected Flow delivers nothing, and why', () => {
    const html = render(answer({ flows: [flow({ selected: true, delivers: false, active: false })] }))
    expect(html).toContain('Selected, but its logs go nowhere')
    expect(html).toContain('the Flow is inactive')
    expect(html).not.toContain('Shipped by')
    expect(html).not.toContain('no Flow selects this pod')
    const missing = render(answer({ flows: [flow({ selected: true, delivers: false, outputs: [{ kind: 'ClusterOutput', name: 'loki', found: false }] })] }))
    expect(missing).toContain('no output it names exists and is active')
  })
  it('never offers an excluding Flow as the closest match', () => {
    const html = render(answer({ flows: [flow({ name: 'no-debug', excluded: true }), flow({ name: 'optin', unmet: ['label a=1'] })] }))
    expect(html).toContain('Closest match: optin')
    expect(html).toContain('Excluded by no-debug')
  })
  it('links outputs with the namespace the server resolved, and not when it is unknown', () => {
    const navigated: any[] = []
    const html = renderToString(<PodLogShippingSection data={answer({ shipped: true, flows: [flow({ selected: true, delivers: true, outputs: [
      { kind: 'ClusterOutput', namespace: 'logging-system', name: 'loki', type: 'loki', found: true, active: true },
      { kind: 'ClusterOutput', name: 'gone', found: false },
    ] })] })} loading={false} error={null} onNavigate={(r) => navigated.push(r)} />)
    expect(html).toMatch(/<button[^>]*>loki \(loki\)/)
    expect(html).not.toMatch(/<button[^>]*>gone/)
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
