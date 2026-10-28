// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useAgentSignin, type AgentSigninCheck } from './useAgentSignin'
import type { AgentSigninStatus } from '../api/diagnose'

const mocks = vi.hoisted(() => ({
  support: 'supported' as 'supported' | 'unsupported' | 'unknown',
  fetch: vi.fn<(agent: string, refresh: boolean) => Promise<AgentSigninStatus>>(),
}))
vi.mock('../api/client', () => ({
  useRadarFeature: () => ({ support: mocks.support, gatedKey: [], guard: (fn: () => unknown) => fn() }),
}))
vi.mock('../api/diagnose', () => ({
  fetchAgentSignin: (agent: string, refresh: boolean) => mocks.fetch(agent, refresh),
}))

let latest: AgentSigninCheck
function Probe({ agent, enabled }: { agent: string; enabled: boolean }) {
  latest = useAgentSignin(agent, enabled)
  return null
}

// Lets React Query settle: a resolved fetch reaches the component a few
// ticks later.
async function settle() {
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
  }
}

async function mount(agent = 'claude', enabled = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRoot(document.createElement('div'))
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <Probe agent={agent} enabled={enabled} />
      </QueryClientProvider>,
    )
  })
  await settle()
  return root
}

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
})
afterEach(() => {
  mocks.support = 'supported'
  mocks.fetch.mockReset()
})

it('reports a signed-out agent with its sign-in command', async () => {
  mocks.fetch.mockResolvedValue({ agent: 'claude', state: 'signed-out', signinCmd: 'claude auth login' })
  const root = await mount()
  expect(mocks.fetch).toHaveBeenCalledWith('claude', false)
  expect(latest.status).toEqual({ agent: 'claude', state: 'signed-out', signinCmd: 'claude auth login' })
  root.unmount()
})

it.each<AgentSigninStatus['state']>(['signed-in', 'unknown'])('passes a %s answer through without a sign-in command', async (state) => {
  mocks.fetch.mockResolvedValue({ agent: 'claude', state })
  const root = await mount()
  expect(latest.status?.state).toBe(state)
  expect(latest.status?.signinCmd).toBeUndefined()
  root.unmount()
})

it('leaves the consent step alone when the check fails', async () => {
  mocks.fetch.mockRejectedValue(new Error('agent sign-in: 501'))
  const root = await mount()
  expect(latest.status).toBeUndefined()
  root.unmount()
})

it('asks nothing when disabled, without an agent, or on a Radar without the endpoint', async () => {
  for (const [agent, enabled, support] of [
    ['claude', false, 'supported'],
    ['', true, 'supported'],
    ['claude', true, 'unsupported'],
  ] as const) {
    mocks.support = support
    const root = await mount(agent, enabled)
    expect(latest.status).toBeUndefined()
    root.unmount()
  }
  expect(mocks.fetch).not.toHaveBeenCalled()
})

it('re-check bypasses the cache and replaces the answer', async () => {
  mocks.fetch.mockResolvedValueOnce({ agent: 'claude', state: 'signed-out', signinCmd: 'claude auth login' })
  const root = await mount()
  mocks.fetch.mockResolvedValueOnce({ agent: 'claude', state: 'signed-in' })
  await act(async () => latest.recheck())
  await settle()
  expect(mocks.fetch).toHaveBeenLastCalledWith('claude', true)
  expect(latest.status?.state).toBe('signed-in')
  expect(latest.rechecking).toBe(false)
  root.unmount()
})

it('drops a stale signed-out answer when the re-check fails', async () => {
  mocks.fetch.mockResolvedValueOnce({ agent: 'claude', state: 'signed-out', signinCmd: 'claude auth login' })
  const root = await mount()
  mocks.fetch.mockRejectedValueOnce(new Error('network'))
  await act(async () => latest.recheck())
  await settle()
  expect(latest.status?.state).not.toBe('signed-out')
  root.unmount()
})

it('checks again when the user comes back to the window while signed out', async () => {
  mocks.fetch.mockResolvedValueOnce({ agent: 'claude', state: 'signed-out', signinCmd: 'claude auth login' })
  const root = await mount()
  mocks.fetch.mockResolvedValueOnce({ agent: 'claude', state: 'signed-in' })
  await act(async () => {
    window.dispatchEvent(new Event('focus'))
  })
  await settle()
  expect(mocks.fetch).toHaveBeenLastCalledWith('claude', true)
  expect(latest.status?.state).toBe('signed-in')
  root.unmount()
})
