import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, fetchResourceWithRelationships, fetchWorkloadImages, setWorkloadImages } from './client'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('workload image API', () => {
  it('loads the exact referenced workload and API group for ownership checks', async () => {
    const response = { resource: { kind: 'Deployment' }, relationships: { managedBy: [] } }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      expect(input).toBe('/api/resources/deployments/prod/shared?group=apps')
      return Promise.resolve(new Response(JSON.stringify(response), { status: 200 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      fetchResourceWithRelationships('deployments', 'prod', 'shared', 'apps'),
    ).resolves.toEqual(response)
  })

  it('loads the authoritative image inventory', async () => {
    const inventory = {
      target: { group: 'apps', resource: 'deployments', kind: 'Deployment', namespace: 'prod', name: 'web' },
      containers: [{ type: 'container', name: 'app', image: 'repo/app:v1' }],
      behavior: { type: 'rolling' },
    }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      expect(input).toBe('/api/workloads/deployments/prod/web/images')
      return Promise.resolve(new Response(JSON.stringify(inventory), { status: 200 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(fetchWorkloadImages('deployments', 'prod', 'web')).resolves.toEqual(inventory)
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/workloads/deployments/prod/web/images')
  })

  it('posts compare-and-swap image updates and preserves conflict status', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      expect(input).toBe('/api/workloads/deployments/prod/web/images')
      expect(init?.method).toBe('POST')
      return Promise.resolve(new Response(JSON.stringify({
        error: 'image for container "app" changed; review the latest images before applying',
      }), { status: 409, headers: { 'Content-Type': 'application/json' } }))
    })
    vi.stubGlobal('fetch', fetchMock)

    const promise = setWorkloadImages({
      kind: 'deployments',
      namespace: 'prod',
      name: 'web',
      updates: [{ type: 'container', name: 'app', previousImage: 'repo/app:v1', image: 'repo/app:v2' }],
    })
    await expect(promise).rejects.toMatchObject({ status: 409 } satisfies Partial<ApiError>)

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({
      updates: [{ type: 'container', name: 'app', previousImage: 'repo/app:v1', image: 'repo/app:v2' }],
    })
  })
})
