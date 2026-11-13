import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { useArgoStatus } from '../../api/client'
import type { IntegrationProfile } from './LocalConnectionSettings'
import { LocalIntegrationStatus } from './LocalIntegrationStatus'

vi.mock('../../api/client', () => ({
  usePrometheusStatus: () => ({}),
  useOpenCostSummary: () => ({}),
}))
const loki = vi.hoisted(() => ({ value: {} as Record<string, unknown> }))
vi.mock('../../api/loki', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../api/loki')>()), useLokiStatus: () => loki.value }))

const profile: IntegrationProfile = {
  target: {
    binding: 'opaque',
    context: 'development',
    source: '/configs/team',
    inFileName: 'dev',
    fingerprint: 'target',
    clientGeneration: 1,
    operationGeneration: 1,
    identity: { server: 'https://kubernetes', user: 'developer', trust: 'trust', insecureTls: false },
  },
  revision: 'revision',
  state: 'saved',
  mode: 'auto',
  url: '',
  headerKeys: [],
  envHeaderKeys: [],
  headersManaged: false,
  secretSet: true,
  insecureTls: false,
  clusterId: '',
}

const render = (data: Record<string, unknown>) => {
  const argo = { data, isError: false, isPlaceholderData: false, isFetching: false, dataUpdatedAt: 1, errorUpdatedAt: 0 } as unknown as ReturnType<typeof useArgoStatus>
  return renderToStaticMarkup(<LocalIntegrationStatus kind="argocd" profile={profile} argo={argo} busy={false} changedAt={0} />)
}

describe('LocalIntegrationStatus for Argo CD', () => {
  it('says Connecting while the first connection is being made', () => {
    const html = render({ configured: true, connected: false, connecting: true })
    expect(html).toContain('Connecting…')
    expect(html).not.toContain('Not connected')
  })

  it('shows why the last read failed on a working connection', () => {
    const html = render({ configured: true, connected: true, address: 'https://argocd', reason: "Radar's last read from Argo CD failed: the request timed out." })
    expect(html).toContain('Connected')
    expect(html).toContain('the request timed out')
  })
})

describe('LocalIntegrationStatus for Loki', () => {
  const renderLoki = (data: Record<string, unknown>, extra: Record<string, unknown> = {}) => {
    loki.value = { data, isError: false, isPlaceholderData: false, isFetching: false, dataUpdatedAt: 1, errorUpdatedAt: 0, ...extra }
    const argo = {} as ReturnType<typeof useArgoStatus>
    return renderToStaticMarkup(<LocalIntegrationStatus kind="loki" profile={{ ...profile, secretSet: false }} argo={argo} busy={false} changedAt={0} />)
  }
  it('shows the discovered address and the stream labels it found', () => {
    const html = renderLoki({ configured: true, connected: true, address: 'http://localhost:4100', source: 'discovered', labels: { namespace: 'namespace', pod: 'pod', container: 'container', job: 'job_name' } })
    expect(html).toContain('Connected')
    expect(html).toContain('Discovered: http://localhost:4100')
    expect(html).toContain('Stream labels: namespace, pod, container, job job_name')
  })
  it('says why History is unavailable', () => {
    const html = renderLoki({ configured: true, connected: true, address: 'http://loki', source: 'manual', labels: { namespace: '', pod: '', container: '' }, historyReason: 'Loki has no namespace or pod stream label; History needs both' })
    expect(html).toContain('History needs both')
  })
  it('shows the connection error', () => {
    expect(renderLoki({ configured: true, connected: false, discovering: false, error: 'no Loki Service found in the cluster' })).toContain('no Loki Service found in the cluster')
  })
})
