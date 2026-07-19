import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { ChecksViewTabs } from './ChecksViewTabs'

describe('ChecksViewTabs', () => {
  it('keeps browsing scope across tabs without leaking upgrade-only target state', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/checks/upgrade?namespaces=shop%2Cprod&target=1.36&resource=shop%2Fapi']}>
        <ChecksViewTabs />
      </MemoryRouter>,
    )

    expect(html).toContain('href="/checks?namespaces=shop%2Cprod"')
    expect(html).toContain('href="/checks/upgrade?namespaces=shop%2Cprod&amp;target=1.36"')
    expect(html).not.toContain('resource=')
    expect(html).toContain('aria-current="page"')
    expect(html).not.toContain('role="tab"')
  })
})
