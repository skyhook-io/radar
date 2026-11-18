import { describe, expect, it } from 'vitest'
import { readFileSync } from 'fs'
import { join } from 'path'
import { CELL_PADDING_PX, type MeasureText, GROW_MAX_PX, columnSizingSpec, hasCuratedCellWidth, measureCellText, measureColumnContent, workloadImagesText } from './column-content-width'

// The curated grow flags, parsed from the source as curated-column-ownership.test.ts does.
function curatedGrowColumns(): string[] {
  const src = readFileSync(join(__dirname, 'ResourcesView.tsx'), 'utf8')
  const start = src.indexOf('const KNOWN_COLUMNS')
  const body = src.slice(start, src.indexOf('\n}\n', start))
  const found: string[] = []
  let plural = ''
  for (const line of body.split('\n')) {
    const kind = line.match(/^  '?([a-z0-9.]+)'?: \[/)
    if (kind) plural = kind[1]
    const col = line.match(/key: '([a-zA-Z]+)'.*sizing: 'grow'/)
    if (col) found.push(`${plural}:${col[1]}`)
  }
  return found
}

// 10px per character, 0 for nothing: easy arithmetic.
const measure: MeasureText = (text, _font, limit = Infinity) => Math.min(text.length * 10, limit + 10)
const ctx = { plural: 'events', cellFilterKind: 'events' }
const pod = (name: string, extra: Record<string, any> = {}) => ({ metadata: { name, namespace: 'default', ...extra } })

describe('columnSizingSpec', () => {
  it('fits Name, Namespace and Node, grows flagged columns, and keeps the rest exact', () => {
    expect(columnSizingSpec({ key: 'name' })).toEqual({ mode: 'fit', max: 480, weight: 2 })
    expect(columnSizingSpec({ key: 'namespace' }).mode).toBe('fit')
    expect(columnSizingSpec({ key: 'node' }).mode).toBe('fit')
    expect(columnSizingSpec({ key: 'message', sizing: 'grow', growWeight: 3 })).toEqual({ mode: 'grow', weight: 3, max: GROW_MAX_PX })
    expect(columnSizingSpec({ key: 'status' }).mode).toBe('exact')
    expect(columnSizingSpec({ key: 'age' }).mode).toBe('exact')
  })
})

describe('measureColumnContent', () => {
  it('sizes Name to its 95th percentile and reaches for its longest, with chrome and padding', () => {
    const items = [...Array(95)].map((_, i) => pod(`n${String(i).padStart(9, '0')}`)) // 10 chars
    for (let i = 0; i < 5; i++) items.push(pod('x'.repeat(40)))
    const m = measureColumnContent(items, [{ key: 'name' }], ctx, measure)
    // 100px of text + 22px copy button + padding
    expect(m.name.content).toBe(100 + 22 + CELL_PADDING_PX)
    expect(m.name.stretch).toBe(400 + 22 + CELL_PADDING_PX)
  })

  it('trims outliers the same way for Namespace', () => {
    const items = [...Array(39)].map(() => ({ metadata: { name: 'a', namespace: 'apps' } }))
    items.push({ metadata: { name: 'a', namespace: 'x'.repeat(20) } })
    const m = measureColumnContent(items, [{ key: 'namespace' }], ctx, measure)
    expect(m.namespace).toEqual({ content: 40 + CELL_PADDING_PX, stretch: 200 + CELL_PADDING_PX })
  })

  it('reserves room for the Terminating chip and an audit badge beside a name', () => {
    const deleting = pod('abc', { deletionTimestamp: '2026-01-01T00:00:00Z' })
    const flagged = pod('abc')
    const plain = measureColumnContent([pod('abc')], [{ key: 'name' }], ctx, measure).name.content
    expect(measureColumnContent([deleting], [{ key: 'name' }], ctx, measure).name.content).toBeGreaterThan(plain + 90)
    const audited = measureColumnContent([flagged], [{ key: 'name' }], { ...ctx, auditBadges: { 'default/abc': { danger: 1, warning: 0 } } }, measure)
    expect(audited.name.content).toBeGreaterThan(plain)
    const paused = { metadata: { name: 'abc', namespace: 'default', annotations: { 'crossplane.io/paused': 'true' } }, status: { conditions: [{ type: 'Synced', status: 'False', reason: 'ReconcilePaused' }] } }
    expect(measureColumnContent([paused], [{ key: 'name' }], ctx, measure).name.content).toBeGreaterThan(plain + 60)
  })

  it('measures what the cell shows for curated grow columns', () => {
    const event = { metadata: { name: 'e' }, message: 'm'.repeat(30), involvedObject: { kind: 'Pod', name: 'web-1', namespace: 'x' } }
    const m = measureColumnContent([event], [{ key: 'message', sizing: 'grow' }, { key: 'object', sizing: 'grow' }], ctx, measure)
    expect(m.message.content).toBe(300 + CELL_PADDING_PX)
    expect(m.object.content).toBe('Pod/web-1'.length * 10 + CELL_PADDING_PX)
  })

  it('reads printer and custom columns through their own value reader', () => {
    const col = { key: 'printer:Reason', sizing: 'grow' as const, getFilterValue: (r: any) => r.status.reason }
    const m = measureColumnContent([{ metadata: { name: 'a' }, status: { reason: 'abcdef' } }], [col], { plural: 'widgets', cellFilterKind: '__generic_widgets' }, measure)
    expect(m['printer:Reason'].content).toBe(60 + CELL_PADDING_PX)
  })

  it('does not measure exact columns, and stops counting a value past the cap', () => {
    const event = { metadata: { name: 'e' }, message: 'm'.repeat(10_000) }
    const m = measureColumnContent([event], [{ key: 'status' }, { key: 'message', sizing: 'grow' }], ctx, measure)
    expect(m.status).toBeUndefined()
    expect(m.message.content).toBeLessThanOrEqual(GROW_MAX_PX + 10 + CELL_PADDING_PX)
  })

  it('skips a value whose reader throws rather than failing the table', () => {
    const bad = { metadata: { name: 'abc' }, get message(): string { throw new Error('boom') } }
    const m = measureColumnContent([bad], [{ key: 'message', sizing: 'grow' }], ctx, measure)
    expect(m.message.content).toBe(CELL_PADDING_PX)
  })

  it('measures 50k rows quickly enough to run on every list update', () => {
    const items = [...Array(50_000)].map((_, i) => ({
      metadata: { name: `checkout-service-${i}-7c9f8d6b5-abcde`, namespace: `team-${i % 40}` },
      spec: { nodeName: `ip-10-0-${i % 250}-${i % 200}.us-east-1.compute.internal` },
      message: `Back-off restarting failed container app in pod checkout-service-${i}`,
      involvedObject: { kind: 'Pod', name: `checkout-service-${i}` },
    }))
    const cols = [{ key: 'name' }, { key: 'namespace' }, { key: 'node' }, { key: 'message', sizing: 'grow' as const }, { key: 'object', sizing: 'grow' as const }]
    measureColumnContent(items.slice(0, 100), cols, ctx) // warm the glyph cache
    const start = performance.now()
    measureColumnContent(items, cols, ctx)
    const elapsed = performance.now() - start
    // Typically ~40-80ms on a laptop; the bound only catches an accidental O(n²) or per-string canvas call.
    expect(elapsed).toBeLessThan(1500)
  })
})

describe('measureCellText', () => {
  // No canvas here, so every glyph counts at the fallback width: the test is
  // about which glyphs are counted.
  const glyphs = (text: string) => measureCellText(text, 'body') / measureCellText('a', 'body')

  it('collapses whitespace the way a one-line cell renders it', () => {
    expect(glyphs('a\n\n   b\t c ')).toBe(glyphs('a b c'))
    expect(glyphs('plain text')).toBe(10)
  })

  it('counts an emoji as one glyph, not two halves', () => {
    expect(glyphs('ok 🚀')).toBe(4)
  })
})

describe('curated grow columns', () => {
  it('each has a reader for the text its cell shows', () => {
    const flagged = curatedGrowColumns()
    expect(flagged.length).toBeGreaterThan(15)
    expect(flagged.filter(entry => {
      const [plural, column] = entry.split(':')
      return !hasCuratedCellWidth(plural, column)
    })).toEqual([])
  })

  it('shows the first image in full and how many more', () => {
    expect(workloadImagesText([])).toBe('')
    expect(workloadImagesText(['nginx:1.27'])).toBe('nginx:1.27')
    expect(workloadImagesText(['nginx:1.27', 'envoy:1.30', 'otel:0.9'])).toBe('nginx:1.27 +2')
  })
})
