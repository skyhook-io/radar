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

describe('dependency relationships', () => {
  it('renders a dependency-only response with the existing navigation control', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ dependencies: [{ kind: 'Issuer', group: 'cert-manager.io', namespace: 'team', name: 'ca' }] }} onNavigate={() => {}} />)
    expect(html).toContain('Depends On')
    expect(html).toContain('ca')
    expect(html).toContain('<button')
    expect(html).not.toContain('Scale Target')
  })

  it('renders the reverse dependency without calling it an autoscaler', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ dependents: [{ kind: 'Certificate', group: 'cert-manager.io', namespace: 'team', name: 'tls' }] }} />)
    expect(html).toContain('Required By')
    expect(html).toContain('tls')
    expect(html).not.toContain('Autoscaler')
  })
})

describe('versioned dependency projections', () => {
  const issuer = { kind: 'Issuer', group: 'cert-manager.io', namespace: 'team', name: 'ca' }
  const certificate = { kind: 'Certificate', group: 'cert-manager.io', namespace: 'team', name: 'tls' }

  it('presents mirrored configuration dependencies once under the specific labels', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ dependencies: [issuer], configRefs: [issuer], dependents: [certificate], consumers: [certificate] }} onNavigate={() => {}} />)
    expect(html).toContain('Depends On')
    expect(html).toContain('Required By')
    expect(html).not.toContain('Configuration')
    expect(html).not.toContain('Used By')
    expect(html.match(/<button/g)).toHaveLength(3)
  })

  it('preserves configuration and consumer groups when dependency fields are absent', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ configRefs: [issuer], consumers: [certificate] }} />)
    expect(html).toContain('Configuration')
    expect(html).toContain('Used By')
    expect(html).not.toContain('Depends On')
  })

  it('retains navigation controls for identically named resources from distinct API groups', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ dependencies: [issuer, { ...issuer, group: 'other.example.com' }, issuer] }} onNavigate={() => {}} />)
    expect(html.match(/<button/g)).toHaveLength(3)
  })
})

describe('management without ownership', () => {
  it('lists management links apart from Owner and Children', () => {
    const gateway = renderToString(
      <RelatedResourcesSection relationships={{ managers: [{ kind: 'GatewayClass', namespace: '', name: 'istio', group: 'gateway.networking.k8s.io' }] }} />
    )
    expect(gateway).toContain('Managed By')
    expect(gateway).toContain('istio')
    expect(gateway).not.toContain('Owner')

    const kustomization = renderToString(
      <RelatedResourcesSection relationships={{ manages: [{ kind: 'Deployment', namespace: 'prod', name: 'web', group: 'apps' }] }} />
    )
    expect(kustomization).toContain('Manages')
    expect(kustomization).not.toContain('Children')
  })
})
