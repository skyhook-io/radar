import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { CostViewTabs } from './CostViewTabs'
import { NavCustomizationProvider } from '../../context/NavCustomization'
import { isRightsizingScanPath } from '../../utils/page-links'

describe('CostViewTabs', () => {
  it('opens the host rightsizing scan with the nested router scope without taking over cost allocation', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/cost?namespaces=shop']}>
        <NavCustomizationProvider value={{
          fleetTakeoverHref: (target, search) => target === 'rightsizing' ? `/rightsizing${search}` : undefined,
        }}>
          <CostViewTabs />
        </NavCustomizationProvider>
      </MemoryRouter>,
    )
    expect(html).toContain('href="/rightsizing?namespaces=shop"')
    expect(html).toContain('href="/cost?namespaces=shop"')
    expect(isRightsizingScanPath('/cost/rightsizing/')).toBe(true)
    expect(isRightsizingScanPath('/cost/rightsizing-other')).toBe(false)
  })

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
