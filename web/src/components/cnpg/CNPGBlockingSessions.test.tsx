import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

vi.mock('../../api/cnpg-sessions', () => ({
  useCNPGSessions: () => ({
    data: { pod: 'pg-1', state: 'denied', permission: { exec: 'denied', grant: { verb: 'create', resource: 'pods', subresource: 'exec', namespace: 'db' } }, instances: [] },
    isLoading: false,
    error: null,
    isRefetchError: false,
    dataUpdatedAt: Date.now(),
  }),
}))

const { CNPGBlockingSessions } = await import('./CNPGBlockingSessions')

describe('CNPGBlockingSessions with exec denied', () => {
  it('points at the aggregate counts only when they are readable', () => {
    const readable = renderToStaticMarkup(<CNPGBlockingSessions namespace="db" cluster="pg" />)
    expect(readable).toContain('The aggregate counts above come from the metrics exporter and still apply')

    const unreadable = renderToStaticMarkup(<CNPGBlockingSessions namespace="db" cluster="pg" aggregatesGap="they need get pods/proxy in db" />)
    expect(unreadable).not.toContain('still apply')
    expect(unreadable).toContain('The aggregate session counts are unavailable too: they need get pods/proxy in db.')
  })
})
