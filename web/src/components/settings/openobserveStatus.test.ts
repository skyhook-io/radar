import { describe, expect, it } from 'vitest'
import { metricsSourceLine, sourceNote } from './openobserveStatus'

describe('metricsSourceLine', () => {
  it('names an ID match on the shared local connection', () => {
    expect(metricsSourceLine({ kind: 'openobserve', via: 'local', matchedBy: 'id' })).toEqual({
      status: 'OpenObserve · shared connection · matched by cluster ID', detail: undefined, tone: 'success',
    })
  })

  it('keeps the standing warning, version warning and missing families for a name match', () => {
    const line = metricsSourceLine({ kind: 'openobserve', via: 'local', matchedBy: 'name', versionWarning: 'Upgrade.', missingFamilies: ['throttling', 'hpa'] })
    expect(line?.status).toBe('OpenObserve · shared connection · matched by confirmed name')
    expect(line?.detail).toBe("Add the cluster ID label so this match can't break. Upgrade. Not collected: CPU throttling, HPA.")
  })

  it('shows the standing warning of an active match after saving', () => {
    const warning = 'This cluster\'s OpenObserve collector sends the default cluster name "cluster1". Radar refuses its kube-state metrics.'
    expect(metricsSourceLine({ kind: 'openobserve', via: 'local', matchedBy: 'id', warning })?.detail).toBe(warning)
    expect(metricsSourceLine({ kind: 'openobserve', via: 'local', matchedBy: 'name', warning: 'Matched by cluster name.' })?.detail).toBe('Matched by cluster name.')
  })

  it('reports unavailable without claiming a connection', () => {
    expect(metricsSourceLine({ kind: 'openobserve', via: 'local', matchedBy: 'id', unavailable: true, error: 'OpenObserve unavailable: refused' }))
      .toEqual({ status: 'OpenObserve unavailable', detail: 'OpenObserve unavailable: refused', tone: 'warning' })
  })

  it('leaves the Prometheus line for an unmatched context and explains why', () => {
    const src = { kind: 'openobserve' as const, via: 'local' as const, error: "This context isn't matched in OpenObserve." }
    expect(metricsSourceLine(src)).toBeUndefined()
    expect(sourceNote(src)).toBe("This context isn't matched in OpenObserve.")
    expect(metricsSourceLine(undefined)).toBeUndefined()
  })
})
