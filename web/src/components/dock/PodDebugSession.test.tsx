// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { PodDebugSession } from './PodDebugSession'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const session = { apiBase: '/api', podUID: 'reviewed', image: 'mirror.example/tools' }
const opts = { namespace: 'demo', podName: 'api', containerName: 'debug-fixture', session }
const key = ['pod-debug-startup', '/api', undefined, 'reviewed', 'debug-fixture']
const client = new QueryClient()
let root: ReturnType<typeof createRoot>
async function mount() {
  const host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><PodDebugSession {...opts}><b>attached</b></PodDebugSession></QueryClientProvider>)
  })
}
afterEach(() => {
  act(() => root.unmount())
  document.body.replaceChildren()
  client.clear()
  vi.unstubAllGlobals()
})
function reply(state: object, uid = 'reviewed') {
  return new Response(JSON.stringify({ resource: {
    metadata: { uid }, status: { phase: 'Running', ephemeralContainerStatuses: [{ name: 'debug-fixture', state }] },
  } }))
}
async function until(phrase: string) {
  await act(async () => { await vi.waitFor(() => expect(client.getQueryState(key)?.fetchStatus).toBe('idle')) })
  await vi.waitFor(() => expect(document.body.textContent).toContain(phrase))
}
it('keeps watching a pull backoff and opens only the accepted container once it runs', async () => {
  const fetch = vi.fn().mockResolvedValueOnce(reply({ waiting: { reason: 'ImagePullBackOff', message: 'registry unavailable' } }))
    .mockResolvedValueOnce(reply({ running: {} }))
  vi.stubGlobal('fetch', fetch)
  await mount()
  await until('ImagePullBackOff')
  expect(document.body.textContent).toContain('registry unavailable')
  expect(document.body.textContent).not.toContain('attached')
  await act(async () => { await client.invalidateQueries({ queryKey: key }) })
  await until('attached')
  expect(fetch.mock.calls.every(([, init]) => !init.method || init.method === 'GET')).toBe(true)
})
it.each([
  [() => reply({ waiting: { reason: 'InvalidImageName' } }), 'Start a new debug session with a valid image reference'],
  [() => reply({ waiting: { reason: 'CreateContainerConfigError', message: 'image runs as root' } }), 'Kubernetes could not start the debug container'],
  [() => reply({ terminated: { exitCode: 17, reason: 'Error' } }), 'exited with code 17'],
  [() => reply({ running: {} }, 'replacement'), 'original Pod was replaced'],
  [() => new Response(JSON.stringify({ error: 'pods is forbidden' }), { status: 403 }), 'Access denied'],
])('keeps a terminal detached when startup cannot succeed (%#)', async (response, message) => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response()))
  await mount()
  await until(message)
  expect(document.body.textContent).not.toContain('attached')
})
