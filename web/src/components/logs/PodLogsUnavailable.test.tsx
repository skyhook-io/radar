import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
vi.mock('../../api/client', () => ({ isNotFoundError: (e: any) => e?.status === 404 }))
const { PodLogsUnavailable } = await import('./PodLogsUnavailable')

describe('PodLogsUnavailable', () => {
  it('says a deleted Pod has no logs left instead of rendering nothing', () => {
    const html = renderToStaticMarkup(<PodLogsUnavailable podName="worker-1" error={Object.assign(new Error('Pod not found'), { status: 404 })} />)
    expect(html).toContain('Radar can&#x27;t find worker-1')
    expect(html).toContain('Kubernetes serves no logs for a deleted Pod')
  })
  it('shows the server reason for any other failure', () => {
    const html = renderToStaticMarkup(<PodLogsUnavailable podName="worker-1" error={Object.assign(new Error('no access to pod logs in namespace ml: requires get pods/log'), { status: 403 })} />)
    expect(html).toContain('Logs for worker-1 are unavailable')
    expect(html).toContain('requires get pods/log')
    expect(html).not.toContain('can&#x27;t find')
  })
})
