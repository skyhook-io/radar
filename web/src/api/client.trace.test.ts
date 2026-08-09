import { afterEach, describe, expect, it, vi } from 'vitest'
import { runInClusterMerged } from './client'

// The in-cluster endpoint returns a JSON body on denial too, and a Hub
// ingress/chi timeout returns HTML. Every branch must surface as a thrown
// error with the truthful message - a partial body painted as a
// server-finalized trace would be a fabricated result.
const respond = (status: number, body: string, contentType = 'application/json') =>
  vi.stubGlobal('fetch', () =>
    Promise.resolve(new Response(body, { status, headers: { 'Content-Type': contentType } })),
  )

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('runInClusterMerged error surfacing', () => {
  it('rejects a denial with the server message', async () => {
    respond(403, JSON.stringify({ error: 'your Radar Cloud role cannot run an in-cluster reachability test' }))
    await expect(runInClusterMerged('Service', 'prod', 'web')).rejects.toThrow(/Cloud role/)
  })

  it('rejects a 200 that carries an error field', async () => {
    respond(200, JSON.stringify({ error: 'no eligible routes' }))
    await expect(runInClusterMerged('Service', 'prod', 'web')).rejects.toThrow('no eligible routes')
  })

  it('rejects a non-JSON gateway failure with the status, not a parse error', async () => {
    respond(504, '<html>Gateway Timeout</html>', 'text/html')
    await expect(runInClusterMerged('Service', 'prod', 'web')).rejects.toThrow('In-cluster test failed (504)')
  })

  it('resolves a clean run with the finalized payload', async () => {
    respond(200, JSON.stringify({ trace: { subject: { kind: 'Service', name: 'web' } }, inClusterTests: [] }))
    await expect(runInClusterMerged('Service', 'prod', 'web')).resolves.toMatchObject({ inClusterTests: [] })
  })
})
