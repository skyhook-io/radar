import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { NodeRenderer } from './NodeRenderer'

const node = {
  metadata: { name: 'kind-worker', labels: {} },
  spec: {},
  status: {
    nodeInfo: {
      osImage: 'Container-Optimized OS',
      architecture: 'amd64',
      kernelVersion: '6.1.0',
      containerRuntimeVersion: 'containerd://1.7',
      kubeletVersion: 'v1.33.0',
      kubeProxyVersion: 'v1.33.0',
    },
    capacity: { cpu: '4', memory: '8Gi', pods: '110' },
    allocatable: { cpu: '4', memory: '7Gi', pods: '110' },
    conditions: [],
  },
}

describe('NodeRenderer metrics', () => {
  it('renders a calm unavailable state when metrics-server is absent', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsUnavailable
        metricsHistory={{
          dataPoints: [],
          metricsUnavailableReason: 'the server could not find the requested resource',
        }}
      />,
    )

    expect(html).toContain('Resource Usage')
    expect(html).toContain('Metrics unavailable')
    expect(html).toContain('Radar cannot read metrics.k8s.io')
    expect(html).toContain('Metrics error details')
    expect(html).not.toContain('Install or repair metrics-server and its APIService')
    expect(html).not.toContain('Metrics collection error')
    expect(html).not.toContain('Collecting metrics data')
  })

  it('does not let stale live metrics hide an unavailable state', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsUnavailable
        metrics={{ usage: { cpu: '100m', memory: '256Mi' }, timestamp: '2026-06-30T00:00:00Z' }}
      />,
    )

    expect(html).toContain('Metrics unavailable')
    expect(html).not.toContain('100m')
    expect(html).not.toContain('Last updated')
  })

  it('keeps buffered historical charts visible under an unavailable notice', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsUnavailable
        metricsHistory={{
          dataPoints: [{ timestamp: '2026-06-30T00:00:00Z', cpu: 100000000, memory: 268435456 }],
        }}
      />,
    )

    expect(html).toContain('Metrics unavailable')
    expect(html).toContain('CPU')
    expect(html).toContain('Memory')
    expect(html).not.toContain('Last updated')
  })

  it('keeps non-absence collection errors visible', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsHistory={{ dataPoints: [], collectionError: 'forbidden: no access to nodes' }}
      />,
    )

    expect(html).toContain('Metrics collection error')
    expect(html).toContain('forbidden: no access to nodes')
  })

  it('warns about non-absence collection errors even when historical samples exist', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsHistory={{
          collectionError: 'forbidden: no access to nodes',
          dataPoints: [{ timestamp: '2026-06-30T00:00:00Z', cpu: 100000000, memory: 268435456 }],
        }}
      />,
    )

    expect(html).toContain('Metrics collection error')
    expect(html).toContain('forbidden: no access to nodes')
    expect(html).toContain('CPU')
  })
})
