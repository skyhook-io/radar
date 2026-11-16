import { beforeEach, expect, it, vi } from 'vitest'
import { useMutation, useQuery } from '@tanstack/react-query'
import { fetchJSON } from './client'
import { useVeleroBackupDeletion, useVeleroDeleteBackup, useVeleroRuns } from './velero'

vi.mock('@tanstack/react-query', () => ({ useQuery: vi.fn(), useMutation: vi.fn(), useQueryClient: vi.fn() }))
vi.mock('./client', () => ({
  apiFetch: vi.fn(),
  fetchJSON: vi.fn(),
  useRadarFeature: () => ({ gatedKey: [], guard: (request: () => Promise<unknown>) => request() }),
}))
beforeEach(() => vi.clearAllMocks())

function lastQuery() { return vi.mocked(useQuery).mock.calls.at(-1)![0] }

it('puts run filters in the URL and key and never reuses another filter’s answer', async () => {
  useVeleroRuns('backups', 'velero', { schedule: 'nightly' })
  const first = lastQuery()
  useVeleroRuns('backups', 'velero', { schedule: 'weekly' })
  const second = lastQuery()
  const request = second.queryFn as (context: { signal: AbortSignal }) => Promise<unknown>
  await request({ signal: new AbortController().signal })
  expect(fetchJSON).toHaveBeenCalledWith('/velero/runs/backups?namespace=velero&schedule=weekly', expect.any(AbortSignal))
  expect(second.queryKey).not.toEqual(first.queryKey)
  const previous = { runs: [] }
  const placeholder = second.placeholderData as (prev: unknown, query: { queryKey: readonly unknown[] }) => unknown
  expect(placeholder(previous, { queryKey: first.queryKey })).toBeUndefined()
  expect(placeholder(previous, { queryKey: second.queryKey })).toBe(previous)
  useVeleroRuns('restores', undefined, { backup: 'source' })
  const restore = lastQuery().queryFn as typeof request
  await restore({ signal: new AbortController().signal })
  expect(fetchJSON).toHaveBeenLastCalledWith('/velero/runs/restores?backup=source', expect.any(AbortSignal))
})

it('looks up deletion by backup UID without a request name or browser operation store', async () => {
  useVeleroBackupDeletion('velero', 'nightly', 'uid-one')
  const options = lastQuery()
  const request = options.queryFn as (context: { signal: AbortSignal }) => Promise<unknown>
  await request({ signal: new AbortController().signal })
  expect(fetchJSON).toHaveBeenCalledWith('/velero/backups/velero/nightly/deletion?uid=uid-one', expect.any(AbortSignal))
  expect(options.enabled).toBe(true)
  useVeleroDeleteBackup()
  const mutation = vi.mocked(useMutation).mock.calls.at(-1)![0]!.mutationFn as (variables: unknown) => Promise<unknown>
  await mutation({ namespace: 'velero', name: 'nightly', uid: 'uid-one' })
  expect(fetchJSON).toHaveBeenLastCalledWith('/velero/backups/velero/nightly/delete', expect.objectContaining({ body: JSON.stringify({ uid: 'uid-one' }) }))
})

it('stops deletion polling and retries on every 4xx, including with a previous reading', () => {
  useVeleroBackupDeletion('velero', 'nightly', 'uid-one')
  const options = lastQuery()
  const poll = options.refetchInterval as (query: { state: { error?: unknown; data?: { state: string } } }) => number | false
  const retry = options.retry as (count: number, error: unknown) => boolean
  for (const status of [400, 401, 403, 404, 408, 409, 429]) {
    expect(poll({ state: { error: { status }, data: { state: 'waiting' } } })).toBe(false)
    expect(retry(0, { status })).toBe(false)
  }
  expect(poll({ state: { error: { status: 503 }, data: { state: 'waiting' } } })).toBe(2000)
  expect(retry(0, { status: 503 })).toBe(true)
  for (const state of ['refused', 'failed', 'deleted', 'unknown']) expect(poll({ state: { data: { state } } })).toBe(false)
})
