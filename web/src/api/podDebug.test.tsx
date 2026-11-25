// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { fetchJSON } from './client'
import { podDebugAccessKey, usePodDebugAccess } from './podDebug'

vi.mock('./client', () => ({ fetchJSON: vi.fn(), useRadarFeature: () => ({ support: 'supported' }) }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root, client: QueryClient, host: HTMLDivElement

function Access({ image }: { image?: string }) {
  const { query } = usePodDebugAccess('demo', 'checkout', 'uid-1', !image, false, image)
  return <>{query.data?.state}</>
}
function render(image?: string) {
  return act(() => root.render(<QueryClientProvider client={client}><Access image={image}/></QueryClientProvider>))
}
beforeEach(() => {
  vi.mocked(fetchJSON).mockReset()
  client = new QueryClient()
  host = document.createElement('div')
  root = createRoot(host)
})
afterEach(() => {
  act(() => root.unmount())
  client.clear()
})

it.each(['denied', 'unavailable'])('rechecks previously %s named access when a namespace grant appears', async state => {
  vi.mocked(fetchJSON).mockResolvedValueOnce({ state }).mockResolvedValueOnce({ state: 'allowed', exec: true, image: 'tools' })
  render()
  await vi.waitFor(() => expect(host.textContent).toBe(state))
  render('tools')
  await vi.waitFor(() => expect(host.textContent).toBe('allowed'))
  expect(fetchJSON).toHaveBeenCalledTimes(2)
})

it('keeps a confirmed named denial when the refreshed review still denies access', async () => {
  vi.mocked(fetchJSON).mockResolvedValue({ state: 'denied', exec: true })
  render()
  await vi.waitFor(() => expect(host.textContent).toBe('denied'))
  render('tools')
  await vi.waitFor(() => expect(fetchJSON).toHaveBeenCalledTimes(2))
  render('tools')
  expect(host.textContent).toBe('denied')
  expect(fetchJSON).toHaveBeenCalledTimes(2)
})

it('requires no named check when nothing contradicts the namespace grant on this Pod', () => {
  client.setQueryData([...podDebugAccessKey('demo', 'checkout', 'old-uid'), false], { state: 'denied' })
  render('tools')
  expect(fetchJSON).not.toHaveBeenCalled()
})
