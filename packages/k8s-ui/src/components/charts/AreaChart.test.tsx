import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { AreaChart } from './AreaChart'
import { layoutAnnotations, ANNOTATION_LABEL_ROWS } from './annotations'
import { formatTimestamp } from './format'
import type { ChartAnnotation, TimeSeries } from './types'

const t0 = 1_788_679_000

function series(points: Array<[number, number | null]>, labels: Record<string, string> = {}): TimeSeries {
  return { labels, dataPoints: points.map(([offset, value]) => ({ timestamp: t0 + offset, value })) }
}

function render(props: Partial<Parameters<typeof AreaChart>[0]> & { series: TimeSeries[] }) {
  return renderToStaticMarkup(
    <AreaChart color="#3b82f6" fillColor="#3b82f622" unit="" {...props} />,
  )
}

function attrs(html: string, tag: string, attr: string): string[] {
  const out: string[] = []
  const re = new RegExp(`<${tag}\\b[^>]*\\s${attr}="([^"]*)"`, 'g')
  for (const match of html.matchAll(re)) out.push(match[1])
  return out
}

describe('AreaChart annotations', () => {
  const base = [series([[0, 1], [600, 2], [1200, 3], [1800, 2]])]

  it('renders a marker and label per annotation inside the domain and none outside', () => {
    const annotations: ChartAnnotation[] = [
      { timestamp: t0 + 600, label: 'ConfigMap api-config', kind: 'change' },
      { timestamp: t0 + 9_000, label: 'outside', kind: 'change' },
    ]
    const html = render({ series: base, annotations })
    expect(html.match(/data-chart-annotation="change"/g)).toHaveLength(1)
    expect(html).toContain('data-chart-annotation-label="visible"')
    expect(html).toContain('ConfigMap api-config')
    expect(html).not.toContain('outside')
  })

  it('stacks overlapping labels into rows and hides labels past the row limit', () => {
    const toX = (ts: number) => 84 + ((ts - t0) / 1800) * 876
    const crowded: ChartAnnotation[] = Array.from({ length: ANNOTATION_LABEL_ROWS + 1 }, (_, i) => ({
      timestamp: t0 + i * 5,
      label: `Change ${i}`,
      kind: 'change',
    }))
    const placed = layoutAnnotations(crowded, toX, { minTs: t0, maxTs: t0 + 1800 }, { left: 84, right: 960 })
    expect(placed.map(p => p.row)).toEqual([0, 1, 2, -1])
    expect(placed.at(-1)?.labelHidden).toBe(true)
    const far: ChartAnnotation[] = [
      { timestamp: t0, label: 'first', kind: 'change' },
      { timestamp: t0 + 1200, label: 'second', kind: 'change' },
    ]
    expect(layoutAnnotations(far, toX, { minTs: t0, maxTs: t0 + 1800 }, { left: 84, right: 960 }).map(p => p.row)).toEqual([0, 0])
    const html = render({ series: base, annotations: crowded })
    expect(html.match(/data-chart-annotation-label="visible"/g)).toHaveLength(ANNOTATION_LABEL_ROWS)
    expect(html.match(/data-chart-annotation-label="hidden"/g)).toHaveLength(1)
  })

  it('keeps a long label readable by truncating it', () => {
    const html = render({
      series: base,
      annotations: [{ timestamp: t0 + 600, label: 'ConfigMap a-very-long-configmap-name-that-goes-on', kind: 'change' }],
    })
    expect(html).toContain('ConfigMap a-very-long-confi…')
  })
})

describe('AreaChart domain and axes', () => {
  it('uses an explicit domain wider than the samples for the X axis', () => {
    const html = render({
      series: [series([[600, 1], [1200, 2]])],
      domain: { start: t0, end: t0 + 3600 },
    })
    const labels = attrs(html, 'text', 'text-anchor')
    expect(labels.length).toBeGreaterThan(0)
    expect(html).toContain(`>${formatTimestamp(t0)}<`)
    expect(html).toContain(`>${formatTimestamp(t0 + 3600)}<`)
    const xs = attrs(html, 'text', 'x').map(Number).filter(x => x >= 84)
    expect(Math.max(...xs)).toBeCloseTo(960, 0)
  })

  it('extends the Y axis below zero and draws a zero line for negative values', () => {
    const html = render({ series: [series([[0, -4], [600, 2], [1200, -1]])] })
    expect(html).toContain('data-chart-zero-line')
    expect(html).toContain('>-4.40<')
    const positive = render({ series: [series([[0, 1], [600, 2]])] })
    expect(positive).not.toContain('data-chart-zero-line')
    expect(positive).toContain('>0<')
  })

  it('breaks the line across null gaps instead of bridging them', () => {
    const html = render({ series: [series([[0, 1], [300, 2], [600, null], [900, 3], [1200, 4]])] })
    const lines = attrs(html, 'path', 'fill').filter(fill => fill === 'none')
    expect(lines).toHaveLength(2)
  })

  it('renders sparse samples without a path until two finite points exist', () => {
    expect(attrs(render({ series: [series([[0, 1]])] }), 'path', 'fill')).toHaveLength(0)
    expect(attrs(render({ series: [series([[0, 1], [3600, 5]])] }), 'path', 'fill').filter(fill => fill === 'none')).toHaveLength(1)
  })

  it('renders nothing for an empty series list', () => {
    expect(render({ series: [] })).toBe('')
  })
})

describe('AreaChart compact layout and count axes', () => {
  const base = [series([[0, 1], [600, 2], [1200, 3], [1800, 2]])]

  it('keeps the full layout for existing callers', () => {
    const html = render({ series: base })
    expect(html).toContain('viewBox="0 0 1000 300"')
    expect(html).toContain('data-chart-layout="full"')
    expect(attrs(html, 'text', 'text-anchor').filter(a => a === 'end')).toHaveLength(5)
  })

  it('draws two ticks per axis, larger text and a taller plot when compact', () => {
    const html = render({
      series: base,
      layout: 'compact',
      annotations: [{ timestamp: t0 + 600, label: 'ConfigMap api-config', kind: 'change' }],
    })
    expect(html).toContain('viewBox="0 0 400 260"')
    expect(html).toContain('data-chart-layout="compact"')
    // Two Y labels plus the right-aligned last X label.
    expect(attrs(html, 'text', 'text-anchor').filter(a => a === 'end')).toHaveLength(3)
    expect(attrs(html, 'text', 'text-anchor').filter(a => a === 'start')).toHaveLength(1)
    expect(attrs(html, 'text', 'text-anchor').filter(a => a === 'middle')).toHaveLength(0)
    expect(html).toContain('font-size="14"')
    expect(html).toContain('data-chart-annotation-label="hidden"')
    expect(html).toContain(`>${formatTimestamp(t0)}<`)
    expect(html).toContain(`>${formatTimestamp(t0 + 1800)}<`)
  })

  it('uses integer ticks with a nice step for count series', () => {
    const zero = render({ series: [series([[0, 0], [600, 0], [1200, 0]])], unit: 'count' })
    expect(zero).toContain('>0<')
    expect(zero).toContain('>1<')
    expect(zero).not.toContain('1.10')
    const restarts = render({ series: [series([[0, 6], [600, 12], [1200, 18]])], unit: 'count' })
    for (const tick of ['0', '5', '10', '15', '20']) expect(restarts).toContain(`>${tick}<`)
    expect(restarts).not.toContain('6.05')
    const compactCount = render({ series: [series([[0, 6], [600, 18]])], unit: 'count', layout: 'compact' })
    expect(attrs(compactCount, 'text', 'text-anchor').filter(a => a === 'end')).toHaveLength(3)
    expect(compactCount).toContain('>20<')
    expect(compactCount).not.toContain('>10<')
  })
})
