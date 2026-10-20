import { beforeEach, expect, it, vi } from 'vitest'
import { useQuery } from '@tanstack/react-query'
import { fetchJSON } from './client'
import { useCNPGClusterActivity, useCNPGOperator, useCNPGWorkspace, useCNPGRuntime } from './cnpg'
import { guardRadarFeature, radarFeatureSupport } from './radarFeatures'

vi.mock('@tanstack/react-query', () => ({ useQuery: vi.fn(), useMutation: vi.fn(), useQueryClient: vi.fn() }))
vi.mock('./client', () => ({
  fetchJSON: vi.fn(),
  useRadarFeature: () => {
    const support = radarFeatureSupport('cnpgWorkspace', { features: { resourceIssues: true } }, 'v1.16.0')
    return { support, gatedKey: ['radar-feature-unsupported'], guard: (request: () => Promise<unknown>) => guardRadarFeature('cnpgWorkspace', support, {}, request) }
  },
}))
beforeEach(() => vi.clearAllMocks())

it('still reads the shipped workspace, operator and activity on a server without the new flag', async () => {
  const kinds = ['clusters', 'backups', 'scheduledBackups', 'poolers', 'databases', 'publications', 'subscriptions', 'imageCatalogs', 'clusterImageCatalogs', 'objectStores', 'pods']
  const workspace = { installed: true, context: 'test', namespaces: ['db'], coverage: Object.fromEntries(kinds.map(k => [k, { state: 'full' }])), objects: Object.fromEntries(kinds.map(k => [k, []])), issues: [], audit: [], backupsOmitted: 0 }
  vi.mocked(fetchJSON).mockResolvedValue(workspace)
  useCNPGWorkspace(['db'])
  useCNPGOperator()
  useCNPGClusterActivity('db', 'pg')
  const queries = vi.mocked(useQuery).mock.calls.map(([options]) => options)
  for (const query of queries) {
    const fn = query.queryFn
    if (typeof fn !== 'function') throw new Error('Expected a query function')
    await fn({ signal: new AbortController().signal } as any)
  }
  expect(vi.mocked(fetchJSON).mock.calls.map(([path]) => path)).toEqual([
    '/cnpg/workspace?namespaces=db', '/cnpg/operator', expect.stringMatching(/^\/cnpg\/clusters\/db\/pg\/activity\?since=.*&limit=500$/),
  ])
  expect(queries[0].queryKey).toEqual(['cnpg', 'workspace', 'db'])
})

it('refuses the new live-runtime contract before fetching from the same old server', async () => {
  useCNPGRuntime('db', 'pg')
  const query = vi.mocked(useQuery).mock.calls[0][0]
  const fn = query.queryFn
  if (typeof fn !== 'function') throw new Error('Expected a query function')
  await expect(fn({ signal: new AbortController().signal } as any)).rejects.toMatchObject({ feature: 'cnpgWorkspace' })
  expect(fetchJSON).not.toHaveBeenCalled()
})
