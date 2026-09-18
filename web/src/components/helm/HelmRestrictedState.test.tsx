import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { AuthMe } from '../../api/client'
import { HelmRestrictedState } from './HelmRestrictedState'

let mockAuthMe: AuthMe | undefined
let mockIsLocal: boolean
vi.mock('../../api/client', () => ({
  useAuthMe: () => ({ data: mockAuthMe }),
}))
vi.mock('../../contexts/CapabilitiesContext', () => ({
  useIsLocalDeployment: () => mockIsLocal,
}))
beforeEach(() => {
  mockAuthMe = undefined
  mockIsLocal = true
})

// renderToString escapes apostrophes, which the copy is full of.
function render(compact = false): string {
  return renderToString(<HelmRestrictedState compact={compact} />).replace(/&#x27;/g, "'")
}

const DOCS_HREF = 'href="https://github.com/skyhook-io/radar/blob/main/docs/in-cluster.md#opt-in-permissions"'

describe('HelmRestrictedState', () => {
  it('in-cluster with auth off: names the chart flag, its blast radius, and the auth alternative', () => {
    mockAuthMe = { authEnabled: false }
    mockIsLocal = false
    const html = render()
    expect(html).toContain("Helm view isn't enabled")
    expect(html).toContain('rbac.secrets=true')
    expect(html).toContain('anyone who can open Radar can reveal them')
    expect(html).toContain('Enable authentication')
    expect(html).toContain(DOCS_HREF)
    expect(html).not.toContain('rbac.helm')
  })

  it('local binary with auth off: the kubeconfig identity is what lacks access, so no chart advice', () => {
    mockAuthMe = { authEnabled: false }
    mockIsLocal = true
    const html = render()
    expect(html).toContain("You don't have access to Helm releases")
    expect(html).toContain('current kubeconfig context')
    expect(html).toContain('switch to a context that already has it')
    expect(html).not.toContain('rbac.secrets')
    expect(html).not.toContain('ServiceAccount')
    expect(html).not.toContain(DOCS_HREF)
  })

  it('with auth on, the grant goes to the impersonated user regardless of where Radar runs', () => {
    for (const isLocal of [true, false]) {
      mockAuthMe = { authEnabled: true, authMode: 'oidc', username: 'dana' }
      mockIsLocal = isLocal
      const html = render()
      expect(html).toContain("You don't have access to Helm releases")
      expect(html).toContain('the grant goes to you, not to Radar')
      expect(html).not.toContain('rbac.secrets')
      expect(html).not.toContain('kubeconfig')
      expect(html).not.toContain(DOCS_HREF)
    }
  })

  it('falls back to the kubeconfig identity while auth status is still unresolved', () => {
    mockAuthMe = undefined
    mockIsLocal = true
    expect(render()).toContain('current kubeconfig context')
  })

  it('compact variant keeps the one-line diagnosis and drops the link and remediation', () => {
    mockAuthMe = { authEnabled: false }
    mockIsLocal = false
    const sa = render(true)
    expect(sa).toContain("Helm view isn't enabled")
    expect(sa).toContain("Radar's ServiceAccount can't read Secrets, where Helm stores releases.")
    expect(sa).not.toContain('<a ')
    expect(sa).not.toContain('rbac.secrets')

    mockIsLocal = true
    expect(render(true)).toContain("Your kubeconfig identity can't list Secrets, where Helm stores releases.")

    mockAuthMe = { authEnabled: true }
    const user = render(true)
    expect(user).toContain("Your Kubernetes identity can't list Secrets, where Helm stores releases.")
    expect(user).not.toContain('<a ')
  })
})
