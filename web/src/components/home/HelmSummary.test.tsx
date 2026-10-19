import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { DashboardHelmSummary } from '../../api/client'
import type { HelmReadIdentity } from '../helm/helmReadIdentity'
import { HelmSummary } from './HelmSummary'

let mockIdentity: HelmReadIdentity = 'serviceAccount'
vi.mock('../helm/helmReadIdentity', () => ({
  useHelmReadIdentity: () => mockIdentity,
}))
beforeEach(() => {
  mockIdentity = 'serviceAccount'
})

function render(data: Partial<DashboardHelmSummary>): string {
  const full: DashboardHelmSummary = { total: 0, releases: [], ...data }
  return renderToString(<HelmSummary data={full} onNavigate={() => {}} />).replace(/&#x27;/g, "'")
}

describe('HelmSummary', () => {
  it('restricted in-cluster: blames the ServiceAccount, not the user', () => {
    const html = render({ restricted: true })
    expect(html).toContain("Can't list Helm releases")
    expect(html).toContain("Radar's ServiceAccount can't list Secrets")
  })

  it('restricted for a user identity: blames that identity', () => {
    mockIdentity = 'user'
    const html = render({ restricted: true })
    expect(html).toContain("Your Kubernetes identity can't list Secrets")
  })

  it('restricted before auth status is known: names no identity', () => {
    mockIdentity = 'pending'
    const html = render({ restricted: true })
    expect(html).toContain("Radar can't list Secrets")
    expect(html).not.toContain('ServiceAccount')
  })

  it('rejected credentials: says so instead of blaming permissions', () => {
    const html = render({ error: 'The cluster rejected Radar\'s credentials.', errorCode: 'auth_required' })
    expect(html).toContain('Cluster rejected credentials')
    expect(html).not.toContain("Can't list Helm releases")
  })

  it('unconfigured no longer points at the Helm write flag', () => {
    const html = render({ error: 'Helm client not initialized', errorCode: 'unconfigured' })
    expect(html).toContain('Helm not connected')
    expect(html).not.toContain('rbac.helm')
  })
})
