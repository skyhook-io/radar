import { describe, expect, it } from 'vitest'
import { baseSubtitle, knativeDomainsTooltipLines } from './K8sResourceNode'

describe('baseSubtitle', () => {
  it('describes workflow templates without dropping newer topology kinds', () => {
    expect(baseSubtitle('WorkflowTemplate', { entrypoint: 'main', templateCount: 3 })).toBe('main • 3 templates')
    expect(baseSubtitle('ClusterWorkflowTemplate', { templateCount: 2 })).toBe('2 templates')
    expect(baseSubtitle('ServiceAccount', {})).toBe('Workload identity')
    expect(baseSubtitle('ServiceMonitor', { endpointCount: 1 })).toBe('1 scrape endpoint')
  })

  it('describes a Service by its first port, capping additional ports to a count', () => {
    expect(baseSubtitle('Service', { type: 'ClusterIP', ports: [] })).toBe('ClusterIP')
    expect(baseSubtitle('Service', { type: 'ClusterIP', ports: [{ port: 80 }] })).toBe('ClusterIP :80')
    expect(baseSubtitle('Service', {
      type: 'ClusterIP',
      ports: [{ port: 80 }, { port: 443 }, { port: 9153 }],
    })).toBe('ClusterIP :80 +2 more')
  })

  it('describes a domain-mapped Knative Service by its custom domain', () => {
    expect(baseSubtitle('KnativeService', { url: 'http://api.team.svc.cluster.local' })).toBe('api.team.svc.cluster.local')
    expect(baseSubtitle('KnativeService', { url: 'http://api.team.svc.cluster.local', mappedDomains: ['api.example.com'] })).toBe('api.example.com')
    expect(baseSubtitle('KnativeService', { mappedDomains: ['a.example.com', 'b.example.com'] })).toBe('a.example.com +1')
  })

  it('labels a mapped Knative Service URL by what it is', () => {
    const text = (data: Record<string, unknown>) => knativeDomainsTooltipLines(data).map((l) => l.text)
    // Private: the Service URL is the internal address, shown once.
    expect(text({ mappedDomains: ['api.example.com'], url: 'http://api.team.svc.cluster.local', internalUrl: 'http://api.team.svc.cluster.local' }))
      .toEqual(['api.example.com', 'Internal: http://api.team.svc.cluster.local'])
    // Public: its own URL isn't internal.
    expect(text({ mappedDomains: ['shop.example.com'], url: 'https://shop.team.example.org', internalUrl: 'http://shop.team.svc.cluster.local' }))
      .toEqual(['shop.example.com', 'Service URL: https://shop.team.example.org', 'Internal: http://shop.team.svc.cluster.local'])
    expect(text({ url: 'https://shop.team.example.org' })).toEqual([])
  })

  it('describes a Calico staged deletion as a removal, not as selecting everything', () => {
    // A staged deletion carries no selector, because the Calico API forbids one.
    // Falling through to the empty-selector default would label the broadest
    // possible protection onto a policy that is being taken away.
    expect(baseSubtitle('CalicoStagedNetworkPolicy', { stagedAction: 'Delete' })).toBe('staged deletion')
    expect(baseSubtitle('CalicoStagedGlobalNetworkPolicy', { stagedAction: 'Delete' })).toBe('staged deletion')
    expect(baseSubtitle('CalicoStagedNetworkPolicy', { stagedAction: 'Set', selector: "app == 'web'" })).toBe("app == 'web'")
    expect(baseSubtitle('CalicoNetworkPolicy', {})).toBe('all workloads')
  })
})
