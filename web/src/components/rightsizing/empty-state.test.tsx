import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ScanEmptyState } from './RightsizingScanView'

const props = {
  counts: { increase: 0, reduction: 0, review: 0, need_data: 0, in_range: 2 },
  classFilter: 'actions' as const,
  hasResultFilters: false,
  onlySystemRowsAreHidden: false,
  hasQueryErrors: false,
  onSelect: () => {},
  onClear: () => {},
  onIncludeSystem: () => {},
}

describe('rightsizing empty journeys', () => {
  it('affirms no actionable changes only for evaluated evidence, with an inspection action', () => {
    const html = renderToStaticMarkup(<ScanEmptyState {...props} />)
    expect(html).toContain('No actionable changes')
    expect(html).toContain('2 evaluated containers')
    expect(html).toContain('outside the scan coverage')
    expect(html).toContain('View evaluated containers')
    expect(html).not.toContain('Show recommended actions')
  })

  it.each([false, true])(
    'explains unknown evidence (query errors: %s) without claiming health',
    (hasQueryErrors) => {
      const html = renderToStaticMarkup(
        <ScanEmptyState
          {...props}
          counts={{ ...props.counts, in_range: 0, need_data: 2 }}
          hasQueryErrors={hasQueryErrors}
        />,
      )
      expect(html).toContain('Not enough evidence for recommendations')
      expect(html).toContain('View containers needing evidence')
      expect(html).toContain(
        hasQueryErrors ? 'Some metrics queries failed' : 'not enough usable CPU and memory history',
      )
      expect(html).not.toContain('No actionable changes')
    },
  )

  it('qualifies a mixture of in-range and unknown containers', () => {
    const html = renderToStaticMarkup(
      <ScanEmptyState {...props} counts={{ ...props.counts, need_data: 1 }} />,
    )
    expect(html).toContain('No actionable changes in available evidence')
    expect(html).toContain('not a complete assessment')
    expect(html).toContain('View containers needing evidence')
  })

  it('does not blame missing history when a search or category filter hides results', () => {
    for (const override of [{ hasResultFilters: true }, { classFilter: 'increase' as const }]) {
      const html = renderToStaticMarkup(<ScanEmptyState {...props} {...override} />)
      expect(html).toContain('No results match the current filters')
      expect(html).toContain('Clear filters')
    }
  })

  it('retains the system-workload recovery action', () => {
    const html = renderToStaticMarkup(<ScanEmptyState {...props} onlySystemRowsAreHidden />)
    expect(html).toContain('System workloads are hidden')
    expect(html).toContain('Include system workloads')
  })
})
