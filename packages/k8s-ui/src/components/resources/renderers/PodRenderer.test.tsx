import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PodRenderer } from './PodRenderer'
import { ContainerEnvironmentSection } from './ContainerEnvironmentSection'
import { resolvedEnvFromKey } from '../../../utils/env-from'
import type { ResolvedEnvFrom } from '../../../types'
import type { PodEnvironmentResponse } from '../../../types'

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

describe('PodRenderer resolved environment', () => {
  it('explains Secret coverage without placing Secret values in the initial markup', () => {
    const environment: PodEnvironmentResponse = {
      containers: [{
        name: 'api',
        role: 'container',
        rows: [
          { name: 'PUBLIC_URL', value: 'https://example.com', state: 'resolved', source: { kind: 'ConfigMap', name: 'shared', key: 'PUBLIC_URL' } },
          { name: 'API_TOKEN', state: 'masked', sensitive: true, source: { kind: 'Secret', name: 'shared', key: 'API_TOKEN' } },
        ],
      }],
      coverage: { observedSince: '2026-08-02T00:00:00Z' },
    }

    const html = renderToString(
      <PodRenderer
        data={pod}
        onCopy={() => undefined}
        copied={null}
        environment={environment}
        onRevealEnvironment={async () => ({ value: 'sentinel-secret-value', encoding: 'utf8' })}
      />,
    )

    expect(html).toContain('2 variables')
    expect(html).toContain('1 from Secrets')
    expect(html).toContain('Secret values stay hidden until you reveal them.')
    expect(html).not.toContain('Value if restarted now')
    expect(html).toContain('Only variables declared on the Pod are shown.')
    expect(html).not.toContain('Changes observed')
    expect(html).toContain('Secret<!-- -->/<!-- -->shared')
    expect(html).toContain('flex-wrap items-center gap-x-1.5 gap-y-0.5')
    expect(html).toContain('@container/env')
    expect(html).toContain('table-fixed')
    expect(html).toContain('w-[46%]')
    expect(html).toContain('table-divide-subtle')
    expect(html).not.toContain('overflow-x-auto')
    expect(html).not.toContain('space-y-2.5 px-3 py-3 text-xs')
    expect(html).not.toContain('title=')
    expect(html).not.toContain('sentinel-secret-value')
  })

  it('only allocates a status column when a row needs attention', () => {
    const environment: PodEnvironmentResponse = {
      containers: [{
        name: 'api',
        role: 'container',
        rows: [{ name: 'PUBLIC_URL', value: 'https://example.com', state: 'resolved', source: { kind: 'Direct' } }],
      }],
      coverage: {},
    }
    const render = (value: PodEnvironmentResponse) => renderToString(
      <ContainerEnvironmentSection
        environment={value}
        namespace="default"
        onCopy={() => undefined}
        copied={null}
      />,
    )

    const normal = render(environment)
    expect(normal).toContain('w-[46%]')
    expect(normal).not.toContain('>Status</span>')
    expect(normal).toContain('lucide-equal')
    expect(normal).not.toContain('lucide-pencil')

    const optional = render({
      ...environment,
      containers: [{
        ...environment.containers[0],
        rows: [{ name: 'FEATURE', state: 'missing', optional: true, source: { kind: 'ConfigMap', name: 'settings', key: 'feature' }, message: 'Optional key feature is absent.' }],
      }],
    })
    expect(optional).toContain('Not set')
    expect(optional).toContain('w-[46%]')
    expect(optional).not.toContain('>Status</span>')
    expect(optional).toContain('!cursor-help')
    expect(optional).not.toContain('<button class="badge-sm')

    const navigable = renderToString(
      <ContainerEnvironmentSection
        environment={{
          ...environment,
          containers: [{
            ...environment.containers[0],
            rows: [{ name: 'PUBLIC_URL', value: 'https://example.com', state: 'resolved', source: { kind: 'ConfigMap', name: 'settings', key: 'url' } }],
          }],
        }}
        namespace="default"
        onNavigate={() => undefined}
        onCopy={() => undefined}
        copied={null}
      />,
    )
    expect(navigable).toContain('cursor-pointer')
    expect(navigable).toContain('<button class="badge-sm')

    const runtime = render({
      ...environment,
      containers: [{
        ...environment.containers[0],
        rows: [{ name: 'RUNTIME_VALUE', value: '$(SERVICE_HOST)', state: 'unavailable', runtimeDependent: true, source: { kind: 'Direct' } }],
      }],
    })
    expect(runtime).toContain('Set at startup')
    expect(runtime).toContain('w-[46%]')
    expect(runtime).not.toContain('>Status</span>')

    const restartBlocked = render({
      ...environment,
      containers: [{
        ...environment.containers[0],
        rows: [{
          name: 'REQUIRED',
          state: 'missing',
          missingImpact: 'restartBlocked',
          source: { kind: 'ConfigMap', name: 'settings', key: 'required' },
          message: 'The running container cannot restart while this is missing.',
          evidence: { kind: 'removed', changedAt: '2026-08-02T00:00:00Z', message: 'Removed after the container started.' },
        }],
      }],
    })
    expect(restartBlocked).toContain('Restart blocked')
    expect(restartBlocked).not.toContain('>Removed after start</span>')
    expect(restartBlocked).toContain('>Status</span>')

    const startupBlocked = render({
      ...environment,
      containers: [{
        ...environment.containers[0],
        rows: [{ name: 'REQUIRED', state: 'missing', missingImpact: 'startupBlocked', source: { kind: 'Secret', name: 'credentials', key: 'required' } }],
      }],
    })
    expect(startupBlocked).toContain('Prevents start')
    expect(startupBlocked).toContain('>Status</span>')

    const limitedHistory = render({
      ...environment,
      coverage: { degraded: true, degradedReason: 'Change history is limited to this Radar session.', saturated: true },
    })
    expect(limitedHistory).toContain('Change history is limited to this Radar session.')
    expect(limitedHistory).toContain('Some recent changes may not be shown.')

    const changed = render({
      ...environment,
      containers: [{
        ...environment.containers[0],
        rows: [{
          ...environment.containers[0].rows[0],
          evidence: { kind: 'modified', changedAt: '2026-08-02T00:00:00Z', message: 'Changed after the container started.' },
        }],
      }],
    })
    expect(changed).toContain('w-[42%]')
    expect(changed).toContain('>Status</span>')
  })

  it('selects the first regular container instead of an init container', () => {
    const html = renderToString(
      <ContainerEnvironmentSection
        environment={{
          containers: [
            { name: 'migrate', role: 'init', rows: [{ name: 'INIT_ONLY', value: 'yes', state: 'resolved', source: { kind: 'Direct' } }] },
            { name: 'api', role: 'container', rows: [{ name: 'API_ONLY', value: 'yes', state: 'resolved', source: { kind: 'Direct' } }] },
          ],
          coverage: {},
        }}
        namespace="default"
        onCopy={() => undefined}
        copied={null}
      />,
    )

    expect(html).toContain('API_ONLY')
    expect(html).not.toContain('INIT_ONLY')
    expect(html).toContain('aria-selected="false"')
    expect(html).toContain('aria-selected="true"')
  })

  it('offers copy for concrete values and confirms success', () => {
    const environment: PodEnvironmentResponse = {
      containers: [{
        name: 'api',
        role: 'container',
        rows: [{ name: 'PUBLIC_URL', value: 'https://example.com', state: 'resolved', source: { kind: 'Direct' } }],
      }],
      coverage: {},
    }
    const render = (copied: string | null) => renderToString(
      <ContainerEnvironmentSection
        environment={environment}
        namespace="default"
        onCopy={() => undefined}
        copied={copied}
      />,
    )

    const ready = render(null)
    expect(ready).toContain('group-hover/value:opacity-100')
    expect(ready).toContain('aria-label="Copy value"')
    expect(ready).toContain('lucide-copy')

    const confirmed = render('api\u0000PUBLIC_URL')
    expect(confirmed).toContain('aria-label="Value copied"')
    expect(confirmed).toContain('lucide-check')
    expect(confirmed).toContain('text-green-400')
  })

  it('keeps the declaration view when resolved sources contain no usable variables', () => {
    const html = renderToString(
      <PodRenderer
        data={pod}
        onCopy={() => undefined}
        copied={null}
        environment={{ containers: [{ name: 'api', role: 'container', rows: [] }], coverage: {} }}
      />,
    )

    expect(html).toContain('Environment Variables')
    expect(html).toContain('ConfigMap')
    expect(html).toContain('Secret')
    expect(html).toContain('(all keys)')
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
