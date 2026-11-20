import { afterEach, expect, it, vi } from 'vitest'
import { useQuery } from '@tanstack/react-query'
import { useDiagnostics } from './client'

vi.mock('@tanstack/react-query', async (original) => ({
  ...await original<typeof import('@tanstack/react-query')>(),
  useQuery: vi.fn(() => ({})),
}))

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

it('stops waiting for an unresponsive diagnostics endpoint and allows an explicit retry', async () => {
  vi.useFakeTimers()
  const fetch = vi.fn<typeof globalThis.fetch>()
    .mockImplementationOnce(async (_, init) => new Promise<Response>((_, reject) => {
      const signal = init!.signal!
      signal.addEventListener('abort', () => reject(signal.reason), { once: true })
    }))
    .mockResolvedValueOnce(new Response('{"uptimeSec":42}'))
  vi.stubGlobal('fetch', fetch)

  useDiagnostics(true)
  const options = vi.mocked(useQuery).mock.calls.at(-1)![0]
  const read = options.queryFn as (ctx: { signal: AbortSignal }) => Promise<unknown>
  const result = read({ signal: new AbortController().signal })
  expect(options.retry).toBe(false)
  const timedOut = expect(result).rejects.toThrow('within 15 seconds')
  await vi.advanceTimersByTimeAsync(15000)
  await timedOut
  await expect(read({ signal: new AbortController().signal })).resolves.toEqual({ uptimeSec: 42 })
})
