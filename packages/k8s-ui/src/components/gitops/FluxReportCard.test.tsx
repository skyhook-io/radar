import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { FluxReportCard } from './FluxReportCard'
import type { FluxReportSummary } from '../../utils/flux-report'

const summary = (patch: Partial<FluxReportSummary> = {}): FluxReportSummary => ({
  version: 'v2.7.5', operatorVersion: 'v0.62.1', managedBy: 'flux-operator',
  controllers: Array.from({ length: 5 }, (_, i) => ({ name: `controller-${i}`, tag: 'v1', ready: true })),
  controllersReady: 5, failing: 0,
  rows: [{ kind: 'HelmRelease', total: 143, failing: 0, suspended: 0 }],
  also: [{ kind: 'GitRepository', total: 1 }],
  needsInventoryNote: false, total: 1, ...patch,
})
const render = (s: FluxReportSummary, open = true) => renderToString(<FluxReportCard summary={s} defaultOpen={open} />).replace(/<!-- -->/g, '')

describe('FluxReportCard', () => {
  it('sums Flux up in one line', () => {
    const line = render(summary(), false)
    // The operator writes zeros for a kind it can't list, so the count is its claim.
    expect(line).toContain('Flux v2.7.5 · 5 of 5 controllers ready · Flux Operator reports 0 failing')
    // Ready's lastTransitionTime moves only when Ready flips, not on each report.
    expect(line).not.toContain('reported ')
    const down = summary({ controllersReady: 4 })
    expect(render(down, false)).toContain('4 of 5 controllers ready')
  })

  it('never shows zeros for a report without data', () => {
    const html = render(summary({ controllers: [], controllersReady: undefined, failing: undefined }), false)
    expect(html).toContain('controllers unknown')
    expect(html).not.toContain('0 failing')
  })

  it('shows the operator version without an installer name', () => {
    expect(render(summary({ managedBy: undefined }))).toContain('operator v0.62.1')
  })

  it('opens on first render when the browser kept it open', () => {
    const g = globalThis as any
    const had = 'window' in g
    g.window = { localStorage: { getItem: () => 'true', setItem: () => {} } }
    try {
      expect(renderToString(<FluxReportCard summary={summary()} />)).toContain('aria-expanded="true"')
    } finally {
      if (!had) delete g.window
    }
  })

  it('says when a HelmRelease may have no inventory', () => {
    expect(render(summary({ needsInventoryNote: true }))).toContain('This cluster runs v2.7.5, whose helm-controller records no HelmRelease inventory')
    const current = render(summary({ version: 'v2.8.1', needsInventoryNote: true }))
    expect(current).toContain('Flux records a HelmRelease inventory from version 2.8, at each install or upgrade, so a release installed earlier and not upgraded since has none')
    expect(current).toContain('&quot;Tracked by&quot;')
    expect(render(summary())).not.toContain('inventory')
  })

  it('says which report it shows when there are several', () => {
    expect(render(summary({ total: 2 }), false)).toContain('1 of 2')
    expect(render(summary(), false)).not.toContain('1 of')
  })

  it('lists what Flux reconciles', () => {
    const html = render(summary())
    expect(html).toContain('HelmRelease')
    expect(html).toContain('143')
    expect(html).toContain('Also: GitRepository 1')
    expect(html).toContain('>Total<')
    expect(html).not.toContain('>Running<')
    expect(html).toContain('a kind it can&#x27;t list reads as zero')
  })
})
