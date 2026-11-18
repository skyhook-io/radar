import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import type { APIResource } from '../../types'
import { ChecksViewTabs } from './ChecksViewTabs'

const vulnerabilityReports: APIResource = {
  name: 'vulnerabilityreports', kind: 'VulnerabilityReport', group: 'aquasecurity.github.io', version: 'v1alpha1',
  namespaced: true, verbs: ['list', 'get'], isCrd: true,
}

function render(path: string, resources: APIResource[] = []) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, enabled: false } } })
  client.setQueryData(['api-resources'], resources)
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <ChecksViewTabs />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('ChecksViewTabs', () => {
  it('keeps browsing scope across tabs without leaking upgrade-only target state', () => {
    const html = render('/checks/upgrade?namespaces=shop%2Cprod&target=1.36&resource=shop%2Fapi')

    expect(html).toContain('href="/checks?namespaces=shop%2Cprod"')
    expect(html).toContain('href="/checks/upgrade?namespaces=shop%2Cprod&amp;target=1.36"')
    expect(html).not.toContain('resource=')
    expect(html).toContain('aria-current="page"')
    expect(html).not.toContain('role="tab"')
  })

  it('shows Vulnerabilities only where the cluster serves VulnerabilityReports', () => {
    expect(render('/checks')).not.toContain('Vulnerabilities')

    const html = render('/checks?namespaces=shop', [vulnerabilityReports])
    expect(html).toContain('href="/checks/vulnerabilities?namespaces=shop"')
    expect(html).toContain('Vulnerabilities')
  })

  it('marks Vulnerabilities as the current tab on its route', () => {
    const html = render('/checks/vulnerabilities', [vulnerabilityReports])
    expect(html).toMatch(/aria-current="page"[^>]*href="\/checks\/vulnerabilities"|href="\/checks\/vulnerabilities"[^>]*aria-current="page"/)
  })
})
