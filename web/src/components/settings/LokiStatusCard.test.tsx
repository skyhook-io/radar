import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { RadarFeatureUnsupportedError } from '../../api/radarFeatures'

const state = vi.hoisted(() => ({ value: {} as Record<string, unknown> }))
vi.mock('../../api/loki', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../api/loki')>()), useLokiStatus: () => state.value }))

import { LokiStatusCard } from './LokiStatusCard'

describe('LokiStatusCard', () => {
  it('shows the operator-set connection and its stream labels', () => {
    state.value = { data: { configured: true, connected: true, discovering: false, address: 'http://loki-gateway.loki', labels: { namespace: 'namespace', pod: 'pod', container: 'container' } } }
    const html = renderToStaticMarkup(<LokiStatusCard active />)
    expect(html).toContain('Connected')
    expect(html).toContain('http://loki-gateway.loki')
    expect(html).toContain('Stream labels: namespace, pod, container')
  })
  it('shows an older Radar as an upgrade note, not an error', () => {
    state.value = { error: new RadarFeatureUnsupportedError('lokiConnection', {}) }
    expect(renderToStaticMarkup(<LokiStatusCard active />)).toContain('need a newer Radar')
  })
})
