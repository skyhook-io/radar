import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { WorkloadView } from './WorkloadView'

const workload = {
  apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web', namespace: 'demo' },
  spec: { replicas: 1, selector: { matchLabels: { app: 'web' } }, template: { metadata: { labels: { app: 'web' } }, spec: { containers: [{ name: 'app', image: 'nginx' }] } } },
  status: { replicas: 1, availableReplicas: 1, readyReplicas: 1, updatedReplicas: 1 },
}


describe('workload monitoring projection', () => {
  it('renders monitors in the related dependency card', () => {
    const html = renderToStaticMarkup(<WorkloadView kind="deployments" namespace="demo" name="web" onBack={() => {}} resource={workload} relationships={{ monitors: [{ kind: 'PodMonitor', group: 'monitoring.coreos.com', namespace: 'demo', name: 'scraper' }] }} />)
    expect(html).toContain('Monitored By')
    expect(html).toContain('scraper')
  })
})


describe('workload upstream projection', () => {
  it('keeps other routing sources visible as entrypoints', () => {
    const html = renderToStaticMarkup(<WorkloadView kind="deployments" namespace="demo" name="web" onBack={() => {}} resource={workload} relationships={{ routedFrom: [{ kind: 'Broker', group: 'eventing.knative.dev', namespace: 'demo', name: 'messages' }] }} />)
    expect(html).toContain('Entry point resources')
    expect(html).toContain('messages')
  })
})


describe('workload staged-policy projection', () => {
  it('shows staging without calling it an enforcing network policy', () => {
    const html = renderToStaticMarkup(<WorkloadView kind="deployments" namespace="demo" name="web" onBack={() => {}} resource={workload} relationships={{ stagedPolicies: [{ kind: 'StagedNetworkPolicy', group: 'projectcalico.org', namespace: 'demo', name: 'preview' }] }} />)
    expect(html).toContain('Staged Policies')
    expect(html).toContain('preview')
    expect(html).not.toContain('Network policies')
  })
})


describe('workload GitOps manager', () => {
  const app = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'shop' }
  const render = (relationships: Parameters<typeof WorkloadView>[0]['relationships']) =>
    renderToStaticMarkup(<WorkloadView kind="deployments" namespace="demo" name="web" onBack={() => {}} resource={workload} relationships={relationships} expanded={false} />)

  it('shows the header chip alone when its manager is the only one whose status lists the workload', () => {
    const html = render({ managedBy: [app], managers: [app], ownership: { children: { complete: true } } })
    expect(html).toContain('Managed by')
    expect(html).not.toContain('Managed By')
  })

  it('keeps Managed By when the search left managers it could not settle', () => {
    const html = render({ managedBy: [app], managers: [app], ownership: { children: { complete: true }, managers: { remote: 1 } } })
    expect(html).toContain('Managed By')
    expect(html).toContain('Managed by')
  })

  it('keeps Managed By beside a chip whose manager its status does not list', () => {
    const hub = { ...app, name: 'hub' }
    const html = render({ managedBy: [app], managers: [hub], ownership: { children: { complete: true } } })
    expect(html).toContain('Managed By')
    expect(html).toContain('Tracked by')
    expect(html).toContain("Its tracking label names Application argocd/shop, whose status doesn&#x27;t list it.")
  })

  it('names a manager known only by its instance label without a namespace', () => {
    const hub = { ...app, name: 'hub' }
    const html = render({ managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: '', name: 'shop' }], managers: [hub], ownership: { children: { complete: true } } })
    expect(html).toContain("Its tracking label names Application shop, whose status doesn&#x27;t list it.")
  })
})
