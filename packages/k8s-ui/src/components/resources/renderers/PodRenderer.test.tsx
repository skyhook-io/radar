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
