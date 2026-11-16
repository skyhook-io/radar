import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GenericRenderer } from './GenericRenderer'

const widget = (status: Record<string, unknown>) => ({
  apiVersion: 'example.com/v1', kind: 'Widget', metadata: { name: 'w' }, status,
})

// The status banner uses the shared AlertBanner's light and dark pairs: a raw
// -400 hue reads at roughly 1.5:1 on a light tint.
describe('GenericRenderer status banner', () => {
  it.each([
    ['healthy', { conditions: [{ type: 'Ready', status: 'True' }] }, 'text-emerald-700'],
    ['degraded', { phase: 'Pending' }, 'text-amber-700'],
    ['unhealthy', { conditions: [{ type: 'Ready', status: 'False' }] }, 'text-red-700'],
  ])('%s uses theme-paired colors', (_, status, titleClass) => {
    const html = renderToString(<GenericRenderer data={widget(status)} />)
    const banner = html.slice(0, html.indexOf('</div></div></div>'))
    expect(banner).toContain(titleClass)
    expect(banner).not.toMatch(/text-(yellow|green|red|slate)-400/)
  })

  it('keeps the detail line in neutral text, readable on every tint', () => {
    const html = renderToString(<GenericRenderer data={widget({ conditions: [{ type: 'Ready', status: 'False', reason: 'BackendDown', message: 'the backend is down' }] })} />)
    expect(html).toMatch(/<div class="[^"]*text-theme-text-secondary[^"]*">the backend is down</)
    expect(html).not.toMatch(/text-red-600\/80/)
  })

  it('renders no detail line when the status has no message', () => {
    const html = renderToString(<GenericRenderer data={widget({ conditions: [{ type: 'Ready', status: 'True' }] })} />)
    const banner = html.slice(0, html.indexOf('</div></div></div>'))
    expect(banner).not.toContain('text-theme-text-secondary')
  })
})
