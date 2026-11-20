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
  expect(options.networkMode).toBe('always')
  expect(options.retry).toBe(false)
  const timedOut = expect(result).rejects.toThrow('within 15 seconds')
  await vi.advanceTimersByTimeAsync(15000)
  await timedOut
  await expect(read({ signal: new AbortController().signal })).resolves.toEqual({ uptimeSec: 42 })
})

it('cancels the underlying fetch on close and preserves the cause of transport failures', async () => {
  const failure = new TypeError('Failed to fetch')
  const fetch = vi.fn<typeof globalThis.fetch>()
    .mockImplementationOnce(async (_, init) => new Promise<Response>((_, reject) => {
      init!.signal!.addEventListener('abort', () => reject(init!.signal!.reason), { once: true })
    }))
    .mockRejectedValueOnce(failure)
  vi.stubGlobal('fetch', fetch)
  useDiagnostics(true)
  const read = vi.mocked(useQuery).mock.calls.at(-1)![0].queryFn as (ctx: { signal: AbortSignal }) => Promise<unknown>
  const closing = new AbortController()
  const pending = read({ signal: closing.signal })
  closing.abort()
  await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  await expect(read({ signal: new AbortController().signal })).rejects.toMatchObject({
    message: 'Could not load diagnostics. Failed to fetch',
    cause: failure,
  })
})
