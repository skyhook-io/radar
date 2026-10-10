// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useGitOpsActionCapabilities } from './client'

vi.mock('../context/RadarUpgradeHost', () => ({ useRadarUpgradeHost: () => ({}) }))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
let client: QueryClient
let element: HTMLDivElement
let root: ReturnType<typeof createRoot>
let query: ReturnType<typeof useGitOpsActionCapabilities>
let answer: () => Promise<Response>
const fetcher = vi.fn((input: RequestInfo | URL) => String(input).includes('/gitops/capabilities/') ? answer() : new Promise<Response>(() => {}))

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(['capabilities'], { features: { gitOpsActionCapabilities: true } })
  answer = () => new Promise<Response>(() => {})
  fetcher.mockClear()
  vi.stubGlobal('fetch', fetcher)
  element = document.createElement('div')
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})
async function render(group = 'argoproj.io') {
  function Probe() {
    query = useGitOpsActionCapabilities('applications', group, 'argocd', 'demo')
    return <button disabled={!!query.disabledReasons.sync}>Sync</button>
  }
  await act(async () => root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>))
}
async function refetch() {
  await act(async () => { await query.refetch(); await new Promise(resolve => setTimeout(resolve, 0)) })
}
it('enables controls on initial load, retains allowed results through failed and unknown refetches', async () => {
  let finish: (response: Response) => void
  answer = () => new Promise(resolve => { finish = resolve })
  await render()
  expect(element.querySelector('button')!.disabled).toBe(false)
  await act(async () => { finish!(Response.json({ actions: { sync: { allowed: true } } })); await new Promise(resolve => setTimeout(resolve, 0)) })
  answer = async () => { throw new Error('background poll failed') }
  await refetch()
  expect(query.data?.actions.sync.allowed).toBe(true)
  expect(element.querySelector('button')!.disabled).toBe(false)
  answer = async () => Response.json({ actions: { sync: { reason: "Couldn't check your permissions — retrying" } } })
  await refetch()
  expect(query.data?.actions.sync.allowed).toBe(true)
  expect(element.querySelector('button')!.disabled).toBe(false)
})
it('enables a failed first probe and disables only a definite denial', async () => {
  answer = async () => { throw new Error('offline') }
  await render()
  await new Promise(resolve => setTimeout(resolve, 0))
  expect(element.querySelector('button')!.disabled).toBe(false)
  answer = async () => Response.json({ actions: { sync: { allowed: false, denied: [{ verb: 'patch', resource: 'applications', group: 'argoproj.io', namespace: 'argocd' }] } } })
  await refetch()
  expect(element.querySelector('button')!.disabled).toBe(true)
})
it('replaces a previous result with an unsupported one instead of retaining it', async () => {
  answer = async () => Response.json({ actions: { sync: { allowed: true } } })
  await render()
  await new Promise(resolve => setTimeout(resolve, 0))
  answer = async () => Response.json({ actions: { sync: { unsupported: true, reason: 'Not supported here.' } } })
  await refetch()
  expect(query.data?.actions.sync.unsupported).toBe(true)
  expect(query.disabledReasons.sync).toBe('Not supported here.')
  expect(element.querySelector('button')!.disabled).toBe(true)
})
it('does not fetch Argo capabilities for a KubeVela Application', async () => {
  await render('core.oam.dev')
  expect(fetcher.mock.calls.some(([url]) => String(url).includes('/gitops/capabilities/'))).toBe(false)
})
