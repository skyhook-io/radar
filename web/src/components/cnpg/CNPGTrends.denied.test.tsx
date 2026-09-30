import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

vi.mock('../../api/cnpg-history', async (orig) => ({
  ...(await orig<typeof import('../../api/cnpg-history')>()),
  useCNPGClusterHistory: () => ({ data: { source: 'none', reason: 'Radar is not connected to Prometheus' }, isLoading: false, error: null }),
}))

const { CNPGTrends } = await import('./CNPGTrends')

const render = (samplingDenied?: string) =>
  renderToStaticMarkup(
    <MemoryRouter>
      <CNPGTrends namespace="db" name="pg" samples={[]} samplingDenied={samplingDenied} />
    </MemoryRouter>,
  )

describe('CNPGTrends without Prometheus', () => {
  it('does not wait for samples that can never arrive when the proxy is denied', () => {
    const html = render('get pods/proxy in db')
    expect(html).toContain('History needs Prometheus')
    expect(html).toContain('In-page samples need get pods/proxy in db too.')
    expect(html).not.toContain('Collecting samples')
    expect(html).not.toContain('since this page opened')
  })
  it('samples in the page when the proxy is allowed', () => {
    const html = render()
    expect(html).toContain('Collecting samples')
  })
})
