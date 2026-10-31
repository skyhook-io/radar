// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useAgentSignin, type AgentSigninCheck } from './useAgentSignin'
import type { AgentSigninStatus, ExecutionProfile } from '../api/diagnose'

const mocks = vi.hoisted(() => ({
  support: 'supported' as 'supported' | 'unsupported' | 'unknown',
  fetch: vi.fn<(agent: string, profile: string, refresh: boolean) => Promise<AgentSigninStatus>>(),
}))
vi.mock('../api/client', () => ({
  useRadarFeature: () => ({ support: mocks.support, gatedKey: [], guard: (fn: () => unknown) => fn() }),
}))
vi.mock('../api/diagnose', () => ({
  fetchAgentSignin: (agent: string, profile: string, refresh: boolean) => mocks.fetch(agent, profile, refresh),
}))

const signedOut: AgentSigninStatus = { agent: 'claude', state: 'signed-out', signinCmd: 'claude auth login' }
const signedIn: AgentSigninStatus = { agent: 'claude', state: 'signed-in' }

const seen = {} as { latest: AgentSigninCheck }
function Probe({ agent, profile, enabled }: { agent: string; profile: ExecutionProfile; enabled: boolean }) {
  seen.latest = useAgentSignin(agent, profile, enabled)
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

async function mount(agent = 'claude', enabled = true, profile: ExecutionProfile = 'safeguarded') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRoot(document.createElement('div'))
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <Probe agent={agent} profile={profile} enabled={enabled} />
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

it('reports a signed-out agent with its sign-in command, for the profile in use', async () => {
  mocks.fetch.mockResolvedValue(signedOut)
  const root = await mount('claude', true, 'full-local')
  expect(mocks.fetch).toHaveBeenCalledWith('claude', 'full-local', false)
  expect(seen.latest.status).toEqual(signedOut)
  expect(seen.latest.pending).toBe(false)
  root.unmount()
})

it.each<AgentSigninStatus['state']>(['signed-in', 'unknown'])('passes a %s answer through without a sign-in command', async (state) => {
  mocks.fetch.mockResolvedValue({ agent: 'claude', state })
  const root = await mount()
  expect(seen.latest.status?.state).toBe(state)
  expect(seen.latest.status?.signinCmd).toBeUndefined()
  root.unmount()
})

it('leaves the consent step alone when the check fails', async () => {
  mocks.fetch.mockRejectedValue(new Error('agent sign-in: 501'))
  const root = await mount()
  expect(seen.latest.status).toBeUndefined()
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
    expect(seen.latest.status).toBeUndefined()
    expect(await seen.latest.settle()).toBeUndefined()
    root.unmount()
  }
  expect(mocks.fetch).not.toHaveBeenCalled()
})

it('re-check bypasses the cache and replaces the answer', async () => {
  mocks.fetch.mockResolvedValueOnce(signedOut)
  const root = await mount()
  mocks.fetch.mockResolvedValueOnce(signedIn)
  await act(async () => seen.latest.recheck())
  await settle()
  expect(mocks.fetch).toHaveBeenLastCalledWith('claude', 'safeguarded', true)
  expect(seen.latest.status?.state).toBe('signed-in')
  expect(seen.latest.rechecking).toBe(false)
  root.unmount()
})

it('a failed re-check becomes unknown rather than refetching the stale answer', async () => {
  // The server's cache would keep answering "signed out" to a plain fetch.
  mocks.fetch.mockImplementation(async (_agent, _profile, refresh) => {
    if (refresh) throw new Error('network')
    return signedOut
  })
  const root = await mount()
  const plainFetches = () => mocks.fetch.mock.calls.filter(([, , refresh]) => !refresh).length
  expect(plainFetches()).toBe(1)
  await act(async () => seen.latest.recheck())
  await settle()
  expect(seen.latest.status?.state).toBe('unknown')
  expect(plainFetches()).toBe(1)
  root.unmount()
})

it('applies only the newest of overlapping re-checks', async () => {
  mocks.fetch.mockResolvedValueOnce(signedOut)
  const root = await mount()
  let resolveOlder!: (s: AgentSigninStatus) => void
  mocks.fetch.mockImplementationOnce(() => new Promise((resolve) => { resolveOlder = resolve }))
  mocks.fetch.mockResolvedValueOnce(signedIn)
  await act(async () => seen.latest.recheck())
  await act(async () => seen.latest.recheck())
  await settle()
  expect(seen.latest.status?.state).toBe('signed-in')
  await act(async () => resolveOlder(signedOut))
  await settle()
  expect(seen.latest.status?.state).toBe('signed-in')
  expect(seen.latest.rechecking).toBe(false)
  root.unmount()
})

it('settle waits for the running check', async () => {
  let resolve!: (s: AgentSigninStatus) => void
  mocks.fetch.mockImplementation(() => new Promise((r) => { resolve = r }))
  const root = await mount()
  expect(seen.latest.pending).toBe(true)
  let settled: AgentSigninStatus | undefined
  const waiting = seen.latest.settle().then((s) => { settled = s })
  await settle()
  expect(settled).toBeUndefined()
  await act(async () => resolve(signedOut))
  await waiting
  expect(settled).toEqual(signedOut)
  expect(mocks.fetch).toHaveBeenCalledTimes(1)
  root.unmount()
})

it('settle gives up when the answer never arrives', async () => {
  mocks.fetch.mockImplementation(() => new Promise(() => {}))
  const root = await mount()
  vi.useFakeTimers()
  try {
    let settled = 'waiting' as AgentSigninStatus | undefined | 'waiting'
    const waiting = seen.latest.settle().then((s) => { settled = s })
    await vi.advanceTimersByTimeAsync(2_400)
    expect(settled).toBe('waiting')
    await vi.advanceTimersByTimeAsync(200)
    await waiting
    expect(settled).toBeUndefined()
  } finally {
    vi.useRealTimers()
  }
  root.unmount()
})

it('checks again when the user comes back to the window while signed out', async () => {
  mocks.fetch.mockResolvedValueOnce(signedOut)
  const root = await mount()
  mocks.fetch.mockResolvedValueOnce(signedIn)
  await act(async () => {
    window.dispatchEvent(new Event('focus'))
  })
  await settle()
  expect(mocks.fetch).toHaveBeenLastCalledWith('claude', 'safeguarded', true)
  expect(seen.latest.status?.state).toBe('signed-in')
  root.unmount()
})
