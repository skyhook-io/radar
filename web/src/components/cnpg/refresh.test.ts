import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { refetchCNPGDetail } from './refresh'

it('refreshes active reads for the object and namespace and waits for them', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  let finish!: (value: number) => void
  const pending = new Promise<number>((resolve) => { finish = resolve })
  const reads = [
    ['cnpg', 'runtime', 'db', 'pg'], ['cnpg', 'ha', 'db', 'pg'], ['cnpg', 'storage', 'db', 'pg'],
    ['cnpg', 'history', 'db', 'pg', '1h'], ['cnpg', 'sessions', 'db', 'pg', 'pg-1'],
    ['cnpg', 'workspace', 'db'], ['cnpg', 'workspace', 'db,other'], ['cnpg', 'workspace', ''],
    ['cnpg', 'capabilities', 'clusters', 'db', 'pg'], ['cnpg', 'restore-capability', 'db'],
    ['cnpg', 'runtime', 'db', 'other'], ['cnpg', 'runtime', 'other', 'pg'], ['resource', 'pods'],
    ['cnpg', 'workspace', 'elsewhere'], ['cnpg', 'parameters', 'db', 'pg'],
  ]
  const fns = reads.map((_, i) => vi.fn(() => i === 0 ? pending : Promise.resolve(i)))
  const observers = reads.map((queryKey, i) => {
    client.setQueryData(queryKey, -1)
    const observer = new QueryObserver(client, { queryKey, queryFn: fns[i], staleTime: Infinity })
    return observer.subscribe(() => {})
  })
  const inactive = vi.fn(async () => 1)
  client.setQueryData(['cnpg', 'recovery', 'db', 'pg'], -1)
  client.setQueryDefaults(['cnpg', 'recovery', 'db', 'pg'], { queryFn: inactive })
  let done = false
  const refresh = refetchCNPGDetail(client, { plural: 'clusters', namespace: 'db', name: 'pg', group: 'postgresql.cnpg.io' }).then(() => { done = true })
  await Promise.resolve()
  expect(done).toBe(false)
  for (const i of [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 14]) expect(fns[i]).toHaveBeenCalledTimes(1)
  for (const i of [10, 11, 12, 13]) expect(fns[i]).not.toHaveBeenCalled()
  expect(inactive).not.toHaveBeenCalled()
  finish(1)
  await refresh
  expect(done).toBe(true)
  observers.forEach((off) => off())
  client.clear()
})
