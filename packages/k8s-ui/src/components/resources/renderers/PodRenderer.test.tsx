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
