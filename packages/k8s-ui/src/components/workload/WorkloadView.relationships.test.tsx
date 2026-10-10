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
