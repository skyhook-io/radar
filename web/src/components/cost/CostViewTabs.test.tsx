import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { CostViewTabs } from './CostViewTabs'

describe('CostViewTabs', () => {
  it('preserves namespace and rightsizing filters between cost views', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter
        initialEntries={['/cost/rightsizing?namespaces=shop%2Cprod&rfClass=review&rfQ=api']}
      >
        <CostViewTabs />
      </MemoryRouter>,
    )

    expect(html).toContain('href="/cost?namespaces=shop%2Cprod&amp;rfClass=review&amp;rfQ=api"')
    expect(html).toContain(
      'href="/cost/rightsizing?namespaces=shop%2Cprod&amp;rfClass=review&amp;rfQ=api"',
    )
  })
})
