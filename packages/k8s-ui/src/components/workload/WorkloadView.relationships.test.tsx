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

describe('manager answer group projection', () => {
  const app = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'shop' }
  const object = { kind: 'Deployment', group: 'apps', namespace: 'demo', name: 'web' }
  const answer: import('../../types/core').ManagerAnswer = { state: 'listed', lead: app, direct: { object, recorders: [app] } }
  const render = (manager: typeof answer, extra: Partial<import('../../types/core').Relationships> = {}) =>
    renderToStaticMarkup(<WorkloadView kind="deployments" namespace="demo" name="web" onBack={() => {}} resource={workload} expanded={false}
      relationships={{ managers: [app], ownership: { children: { complete: true }, manager }, ...extra }} />)
  // The group is titled the way the chip words the answer.
  const group = (html: string) => html.includes('Listed By') ? 'Listed By' : html.includes('Managed By') ? 'Managed By' : null

  it.each(['agree', 'listed'] as const)('hides only the exact single %s recorder', state => {
    const html = render({ ...answer, state })
    expect(html).toContain(state === 'agree' ? 'Managed by' : 'Listed by')
    expect(group(html)).toBeNull()
  })
  it.each(['claimed', 'unknown', 'none'] as const)('keeps Managed By for %s', state => {
    expect(group(render({ ...answer, state }))).toBe('Managed By')
  })
  it.each(['disagree', 'shared'] as const)('keeps the group for %s, titled Listed By', state => {
    expect(group(render({ ...answer, state }))).toBe('Listed By')
  })
  it('keeps Managed By with via', () => {
    expect(render({ ...answer, via: { object, recorders: [app] } })).toContain('Managed By')
  })
  it('keeps extra non-recorded managers', () => {
    expect(render(answer, { managers: [app, { kind: 'Certificate', group: 'cert-manager.io', namespace: 'demo', name: 'tls' }] })).toContain('Managed By')
  })
  it('keeps a different single manager', () => {
    expect(render(answer, { managers: [{ ...app, name: 'other' }] })).toContain('Managed By')
  })
  it('keeps the coverage note', () => {
    expect(group(render(answer, { ownership: { children: { complete: true }, manager: answer, managers: { unreadable: 1 } } }))).toBe('Listed By')
  })
  it('uses native Helm’s existing chip', () => {
    const helm = { kind: 'HelmRelease', group: '', namespace: 'demo', name: 'chart' }
    const html = render({ state: 'claimed', lead: helm, direct: { object, claims: [{ manager: helm, source: 'helm-release', check: 'not-recording' }] } })
    expect(html).toContain('Managed by Helm')
  })
})
