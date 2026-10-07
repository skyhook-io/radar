import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { EventsSection, ProblemAlerts, OperationalIssuesShownContext, RelatedResourcesSection } from './drawer-components'

const problems = [
  { color: 'red' as const, message: 'Application is Degraded' },
  { color: 'yellow' as const, message: 'Application is OutOfSync' },
]

describe('ProblemAlerts', () => {
  it('renders every problem', () => {
    const html = renderToString(<ProblemAlerts problems={problems} />)
    expect(html).toContain('Application is Degraded')
    expect(html).toContain('Application is OutOfSync')
  })

  it('renders nothing when there are no problems', () => {
    expect(renderToString(<ProblemAlerts problems={[]} />)).toBe('')
  })

  // Regression guard: ProblemAlerts is used only by GitOps renderers, whose
  // problems the live-Issues pipeline does not comprehensively emit. It must NOT
  // suppress itself under the Operational-Issues context — doing so hid real
  // GitOps warnings (e.g. a manual Argo app's OutOfSync). Pod/Workload renderers
  // self-gate their own arrays; this component never should.
  it('still renders under OperationalIssuesShownContext (does not self-suppress)', () => {
    const html = renderToString(
      <OperationalIssuesShownContext.Provider value={true}>
        <ProblemAlerts problems={problems} />
      </OperationalIssuesShownContext.Provider>
    )
    expect(html).toContain('Application is Degraded')
    expect(html).toContain('Application is OutOfSync')
  })
})

describe('RelatedResourcesSection', () => {
  it('shows incomplete inventory even without resolved relationships', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ warnings: ['Pod relationship inventory unavailable: pods inventory is still syncing'] }} />)
    expect(html).toContain('Related Resources')
    expect(html).toContain('role="status"')
    expect(html).toContain('pods inventory is still syncing')
  })

  it('renders an associated Node', () => {
    const html = renderToString(
      <RelatedResourcesSection
        relationships={{ node: { kind: 'Node', namespace: '', name: 'worker-1' } }}
        onNavigate={() => {}}
      />,
    )

    expect(html).toContain('Related Resources')
    expect(html).toContain('Node')
    expect(html).toContain('worker-1')
    expect(html).toContain('<button')
  })
})

describe('Recent Events layout', () => {
  const events = Array.from({ length: 24 }, (_, i) => ({ id: String(i), source: 'k8s_event', eventType: 'Normal', reason: `Reason${i}`, timestamp: '2026-09-27T00:00:00Z' })) as any
  it('keeps drawer scrolling but lets fullscreen events expand in page flow', () => {
    const drawer = renderToString(<EventsSection events={events} />)
    const fullscreen = renderToString(<EventsSection events={events} fullscreen />)
    expect(drawer).toContain('max-h-64')
    expect(drawer).not.toContain('Show 16 more events')
    expect(fullscreen).not.toContain('max-h-64')
    expect(fullscreen).toContain('Show 16 more events')
    expect(fullscreen).toContain('Reason23')
  })
})
