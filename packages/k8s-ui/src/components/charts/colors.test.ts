import { describe, expect, it } from 'vitest'
import { seriesDisplayLabels } from './colors'

describe('seriesDisplayLabels', () => {
  it('prefers pod, instance or node labels when they are distinct', () => {
    expect(seriesDisplayLabels([
      { labels: { pod: 'api-a', container: 'api' } },
      { labels: { instance: '10.0.0.2:9100' } },
      { labels: { node: 'n1' } },
    ])).toEqual(['api-a', '10.0.0.2:9100', 'n1'])
  })

  it('shows every label when the preferred label is missing or shared', () => {
    expect(seriesDisplayLabels([
      { labels: { container: 'api' } },
      { labels: { container: 'envoy' } },
    ])).toEqual(['container=api', 'container=envoy'])
    expect(seriesDisplayLabels([
      { labels: { pod: 'api-a', container: 'api' } },
      { labels: { pod: 'api-a', container: 'envoy' } },
    ])).toEqual(['pod=api-a, container=api', 'pod=api-a, container=envoy'])
  })

  it('falls back to a positional name for an unlabelled series', () => {
    expect(seriesDisplayLabels([{ labels: {} }, { labels: { pod: 'a' } }])).toEqual(['series-0', 'a'])
  })
})
