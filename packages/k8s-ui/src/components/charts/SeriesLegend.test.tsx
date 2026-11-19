import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { SeriesLegend } from './SeriesLegend'

describe('SeriesLegend', () => {
  const series = [{ labels: { a: '1' }, dataPoints: [] }, { labels: { a: '2' }, dataPoints: [] }]
  it('shows a value beside each name only where one is given', () => {
    const html = renderToStaticMarkup(<SeriesLegend series={series} color="red" seriesLabels={['idle', 'partial']} values={['1.5', undefined]} />)
    expect(html).toMatch(/idle<\/span><span class="tabular-nums[^"]*">1\.5</)
    expect(html).not.toMatch(/partial<\/span><span/)
  })
  it('renders names alone without values', () => {
    expect(renderToStaticMarkup(<SeriesLegend series={series} color="red" seriesLabels={['idle', 'partial']} />)).not.toContain('tabular-nums')
  })
})
