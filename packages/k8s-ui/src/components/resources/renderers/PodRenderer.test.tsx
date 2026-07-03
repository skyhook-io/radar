import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PodRenderer } from './PodRenderer'
import { resolvedEnvFromKey } from '../../../utils/env-from'
import type { ResolvedEnvFrom } from '../../../types'

const pod = {
  metadata: { name: 'api', namespace: 'default' },
  spec: {
    containers: [{
      name: 'api',
      image: 'example/api:latest',
      envFrom: [
        { configMapRef: { name: 'shared' } },
        { secretRef: { name: 'shared' } },
      ],
    }],
  },
  status: { phase: 'Running' },
}

describe('PodRenderer envFrom expansion', () => {
  it('keeps same-name ConfigMap and Secret values separate', () => {
    const resolvedEnvFrom: ResolvedEnvFrom = {
      [resolvedEnvFromKey('configmap', 'shared')]: {
        keys: ['PUBLIC_URL'],
        values: { PUBLIC_URL: 'https://example.com' },
        isSecret: false,
      },
      [resolvedEnvFromKey('secret', 'shared')]: {
        keys: ['API_TOKEN'],
        values: { API_TOKEN: 'secret-value' },
        isSecret: true,
      },
    }

    const html = renderToString(
      <PodRenderer
        data={pod}
        onCopy={() => undefined}
        copied={null}
        resolvedEnvFrom={resolvedEnvFrom}
      />,
    )

    expect(html).toContain('ConfigMap')
    expect(html).toContain('PUBLIC_URL')
    expect(html).toContain('https://example.com')
    expect(html).toContain('Secret')
    expect(html).toContain('API_TOKEN')
    expect(html).not.toContain('PUBLIC_URL<!-- -->=')
  })
})

describe('PodRenderer issues banner', () => {
  it('renders pod status messages for evicted pods', () => {
    const html = renderToString(
      <PodRenderer
        data={{
          metadata: { name: 'nginx', namespace: 'default' },
          spec: { containers: [{ name: 'nginx', image: 'nginx:latest' }] },
          status: {
            phase: 'Failed',
            reason: 'Evicted',
            message: 'Usage of EmptyDir volume "logs-nginx" exceeds the limit "2Gi".',
          },
        }}
        onCopy={() => undefined}
        copied={null}
      />,
    )

    expect(html).toContain('Issues Detected')
    expect(html).toContain('Evicted')
    expect(html).toContain('Usage of EmptyDir volume')
    expect(html).toContain('exceeds the limit')
  })

  it('wraps long issue detail text inside the banner', () => {
    const html = renderToString(
      <PodRenderer
        data={{
          metadata: { name: 'api', namespace: 'default' },
          spec: { containers: [{ name: 'api', image: 'registry.example.com/api:missing' }] },
          status: {
            phase: 'Pending',
            containerStatuses: [
              {
                name: 'api',
                restartCount: 0,
                state: {
                  waiting: {
                    reason: 'ImagePullBackOff',
                    message: `Back-off pulling image "${'a'.repeat(240)}"`,
                  },
                },
              },
            ],
          },
        }}
        onCopy={() => undefined}
        copied={null}
      />,
    )

    expect(html).toContain('ImagePullBackOff')
    expect(html).toContain('min-w-0 break-words')
  })
})

describe('PodRenderer metrics', () => {
  it('renders a calm unavailable state when metrics-server is absent', () => {
    const html = renderToString(
      <PodRenderer
        data={pod}
        onCopy={() => undefined}
        copied={null}
        metricsUnavailable
        metricsHistory={{
          containers: [],
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
      <PodRenderer
        data={pod}
        onCopy={() => undefined}
        copied={null}
        metricsUnavailable
        metrics={{
          containers: [{ name: 'api', usage: { cpu: '100m', memory: '256Mi' } }],
          timestamp: '2026-06-30T00:00:00Z',
        }}
      />,
    )

    expect(html).toContain('Metrics unavailable')
    expect(html).not.toContain('100m')
    expect(html).not.toContain('Last updated')
  })

  it('keeps buffered historical charts visible under an unavailable notice', () => {
    const html = renderToString(
      <PodRenderer
        data={pod}
        onCopy={() => undefined}
        copied={null}
        metricsUnavailable
        metricsHistory={{
          containers: [{
            name: 'api',
            dataPoints: [{ timestamp: '2026-06-30T00:00:00Z', cpu: 100000000, memory: 268435456 }],
          }],
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
      <PodRenderer
        data={pod}
        onCopy={() => undefined}
        copied={null}
        metricsHistory={{ containers: [], collectionError: 'forbidden: no access to pods.metrics.k8s.io' }}
      />,
    )

    expect(html).toContain('Metrics collection error')
    expect(html).toContain('forbidden: no access to pods.metrics.k8s.io')
  })

  it('warns about non-absence collection errors even when historical samples exist', () => {
    const html = renderToString(
      <PodRenderer
        data={pod}
        onCopy={() => undefined}
        copied={null}
        metricsHistory={{
          collectionError: 'forbidden: no access to pods.metrics.k8s.io',
          containers: [{
            name: 'api',
            dataPoints: [{ timestamp: '2026-06-30T00:00:00Z', cpu: 100000000, memory: 268435456 }],
          }],
        }}
      />,
    )

    expect(html).toContain('Metrics collection error')
    expect(html).toContain('forbidden: no access to pods.metrics.k8s.io')
    expect(html).toContain('CPU')
  })
})
