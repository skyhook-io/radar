import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { ChecksViewTabs } from './ChecksViewTabs'

describe('ChecksViewTabs', () => {
  it('keeps namespace scope and target across tabs while dropping unrelated state', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/checks/upgrade?namespaces=shop%2Cprod&target=1.36&resource=shop%2Fapi']}>
        <ChecksViewTabs />
      </MemoryRouter>,
    )

    expect(html).toContain('href="/checks?namespaces=shop%2Cprod&amp;target=1.36"')
    expect(html).toContain('href="/checks/upgrade?namespaces=shop%2Cprod&amp;target=1.36"')
    expect(html).not.toContain('resource=')
    expect(html).toContain('aria-selected="true"')
  })
})
