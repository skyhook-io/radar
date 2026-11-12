import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { FluxReportCard } from './FluxReportCard'
import type { FluxReportSummary } from '../../utils/flux-report'

const summary = (patch: Partial<FluxReportSummary> = {}): FluxReportSummary => ({
  version: 'v2.7.5', operatorVersion: 'v0.62.1', managedBy: 'flux-operator',
  controllers: Array.from({ length: 5 }, (_, i) => ({ name: `controller-${i}`, tag: 'v1', ready: true })),
  controllersReady: 5, failing: 0,
  rows: [{ kind: 'HelmRelease', running: 143, failing: 0, suspended: 0 }],
  also: [{ kind: 'GitRepository', running: 1 }],
  reportedAt: '2026-11-12T09:00:00Z', needsInventoryNote: false, index: 1, total: 1, ...patch,
})
const render = (s: FluxReportSummary, open = true) => renderToString(<FluxReportCard summary={s} defaultOpen={open} />).replace(/<!-- -->/g, '')

describe('FluxReportCard', () => {
  it('sums Flux up in one line', () => {
    expect(render(summary(), false)).toContain('Flux v2.7.5 · 5 controllers ready · 0 failing')
  })

  it('names the 2.8 inventory requirement when it applies', () => {
    const note = 'HelmRelease inventory needs Flux 2.8. This cluster runs v2.7.5, so Radar attributes HelmRelease resources by Flux labels and shows &quot;Tracked by&quot;.'
    expect(render(summary({ needsInventoryNote: true }))).toContain(note)
    expect(render(summary())).not.toContain('needs Flux 2.8')
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
  })
})
