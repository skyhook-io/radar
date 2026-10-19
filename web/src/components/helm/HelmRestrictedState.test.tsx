import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { HelmReadIdentity } from './helmReadIdentity'
import { HelmRestrictedState } from './HelmRestrictedState'

let mockIdentity: HelmReadIdentity = 'serviceAccount'
vi.mock('./helmReadIdentity', () => ({
  useHelmReadIdentity: () => mockIdentity,
}))
beforeEach(() => {
  mockIdentity = 'serviceAccount'
})

// renderToString escapes apostrophes and marks text-node joins with <!-- -->.
function render(compact = false): string {
  return renderToString(<HelmRestrictedState compact={compact} />)
    .replace(/&#x27;/g, "'")
    .replace(/<!-- -->/g, '')
}

describe('HelmRestrictedState', () => {
  it('ServiceAccount: both routes for whoever installed Radar, with the real cost of the chart value', () => {
    const html = render()
    expect(html).toContain("Radar's ServiceAccount isn't allowed to list them")
    expect(html).toContain('Whoever installed Radar')
    expect(html).toContain('Turn on authentication')
    expect(html).toContain('their own Kubernetes access')
    expect(html).toContain('rbac.secrets=true')
    expect(html).toContain('including any ServiceAccount token Secrets')
    expect(html).toContain('if you manage its RBAC yourself')
    expect(html).toContain('href="https://radarhq.io/docs/configuration/in-cluster#opt-in-permissions"')
  })

  it('authenticated user: the access has to be theirs, scoped to the namespaces shown', () => {
    mockIdentity = 'user'
    const html = render()
    expect(html).toContain("your Kubernetes identity can't list Secrets in the namespaces shown")
    expect(html).toContain('Ask whoever administers this cluster')
    expect(html).not.toContain('rbac.secrets')
    expect(html).not.toContain('ClusterRole')
    expect(html).not.toContain('edit')
  })

  it('Radar Cloud viewer: points at the Member role, not Kubernetes RBAC or chart values', () => {
    mockIdentity = 'cloudViewer'
    const html = render()
    expect(html).toContain('the Viewer role can\'t read Secrets')
    expect(html).toContain('Member role')
    expect(html).toContain('href="https://radarhq.io/docs/cloud/rbac"')
    expect(html).not.toContain('rbac.secrets')
  })

  it('other Radar Cloud users: no role advice, since their role may already be Member or Owner', () => {
    mockIdentity = 'cloudUser'
    const html = render()
    expect(html).toContain("Whoever manages Radar's access to this cluster")
    expect(html).not.toContain('Member role')
    expect(html).not.toContain('rbac.secrets')
  })

  it('kubeconfig identity: ask for access or switch context', () => {
    mockIdentity = 'kubeconfig'
    const html = render()
    expect(html).toContain('current kubeconfig context')
    expect(html).toContain('switch to a context that has it')
    expect(html).not.toContain('rbac.secrets')
  })

  it('unknown identity: names no remedy', () => {
    mockIdentity = 'unknown'
    const html = render()
    expect(html).toContain("can't say which access is missing")
    expect(html).not.toContain('rbac.secrets')
    expect(html).not.toContain('Ask whoever')
  })

  it('compact: one line, no link (the dashboard card is itself a button)', () => {
    mockIdentity = 'cloudUser'
    const html = render(true)
    expect(html).toContain("Can't list Helm releases")
    expect(html).toContain("Your cluster access can't list Secrets")
    expect(html).not.toContain('<a ')
  })
})
