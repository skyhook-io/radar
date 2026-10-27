// @vitest-environment jsdom
//
// Cluster-scoped resources have no namespace. Their per-resource URLs send "_"
// in its place to survive proxies that merge repeated slashes. Older Radar
// versions need the empty segment for edit and audit; read, preview and delete
// already support the placeholder.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useMutation, useQuery } from '@tanstack/react-query'
import type { Capabilities } from '@skyhook-io/k8s-ui'
import {
  fetchResourceWithRelationships,
  useBulkDeleteResources,
  useCascadeDeletePreview,
  useDeleteResource,
  useResourceAudit,
  useUpdateResource,
} from './client'

vi.mock('@tanstack/react-query', async (original) => ({
  ...await original<typeof import('@tanstack/react-query')>(),
  useQuery: vi.fn(() => ({})),
  useMutation: vi.fn(() => ({})),
  useQueryClient: vi.fn(() => ({})),
}))

afterEach(() => { vi.clearAllMocks(); vi.unstubAllGlobals() })

function connectedRadar(capabilities: Partial<Capabilities> | undefined) {
  vi.mocked(useQuery).mockImplementation(((options: { queryKey: unknown[] }) =>
    options.queryKey[0] === 'capabilities' ? { data: capabilities, isPending: false } : {}) as unknown as typeof useQuery)
}

function captureRequests() {
  const requests: { method: string; path: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    requests.push({ method: init?.method ?? 'GET', path: new URL(input, 'http://localhost').pathname })
    return init?.method === 'DELETE'
      ? new Response(null, { status: 204 })
      : new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return requests
}

type Ref = { kind: string; namespace: string; name: string }

type UpdateVars = Parameters<ReturnType<typeof useUpdateResource>['mutateAsync']>[0]
type DeleteVars = Parameters<ReturnType<typeof useDeleteResource>['mutateAsync']>[0]
type BulkDeleteVars = Parameters<ReturnType<typeof useBulkDeleteResources>['mutateAsync']>[0]

// Runs the requests the hooks rendered in the test registered with the mocked
// react-query, in the order they were rendered.
async function requestsFor({ kind, namespace, name }: Ref) {
  const requests = captureRequests()
  const [update, del, bulkDelete] = vi.mocked(useMutation).mock.calls.map(call => call[0].mutationFn)
  const [cascadePreview, audit] = vi.mocked(useQuery).mock.calls
    .map(call => call[0] as { queryKey: unknown[]; queryFn: () => Promise<unknown> })
    .filter(options => options.queryKey[0] !== 'capabilities')
    .map(options => options.queryFn)
  await fetchResourceWithRelationships(kind, namespace, name)
  await (update as (vars: UpdateVars) => Promise<unknown>)({ kind, namespace, name, yaml: `kind: X\nmetadata:\n  name: ${name}\n` })
  await (del as (vars: DeleteVars) => Promise<unknown>)({ kind, namespace, name })
  await (bulkDelete as (vars: BulkDeleteVars) => Promise<unknown>)({ items: [{ kind, namespace, name }] })
  await cascadePreview()
  await audit()
  return requests
}

function expected(ref: Ref, readSegment: string, writeSegment: string) {
  const read = `/api/resources/${ref.kind}/${readSegment}/${ref.name}`
  const write = `/api/resources/${ref.kind}/${writeSegment}/${ref.name}`
  return [
    { method: 'GET', path: read },
    { method: 'PUT', path: write },
    { method: 'DELETE', path: read },
    { method: 'DELETE', path: read },
    { method: 'GET', path: `${read}/cascade-preview` },
    { method: 'GET', path: `/api/audit/resource/${ref.kind}/${writeSegment}/${ref.name}` },
  ]
}

const refs: Ref[] = [
  { kind: 'persistentvolumes', namespace: '', name: 'pv-a' },
  { kind: 'nodes', namespace: '', name: 'worker-1' },
  { kind: 'clusterroles', namespace: '', name: 'admin' },
  { kind: 'namespaces', namespace: '', name: 'shop' },
  { kind: 'storageclasses', namespace: '', name: 'standard' },
  { kind: 'clusterissuers', namespace: '', name: 'letsencrypt' },
  { kind: 'configmaps', namespace: 'shop', name: 'app' },
]

describe.each([
  { radar: 'a Radar that reads "_" as cluster-scoped', capabilities: { features: { clusterScopedPlaceholder: true } }, emptyWriteSegment: '_' },
  { radar: 'an older Radar without the flag', capabilities: { features: {} }, emptyWriteSegment: '' },
  { radar: 'a Radar whose capability request failed', capabilities: undefined, emptyWriteSegment: '' },
])('$radar', ({ capabilities, emptyWriteSegment }) => {
  beforeEach(() => connectedRadar(capabilities))

  it.each(refs)('addresses $kind "$namespace"/$name', async (ref) => {
    const { kind, namespace, name } = ref
    useUpdateResource()
    useDeleteResource()
    useBulkDeleteResources()
    useCascadeDeletePreview(kind, namespace, name, undefined, true)
    useResourceAudit(kind, namespace, name)
    expect(await requestsFor(ref)).toEqual(expected(ref, namespace || '_', namespace || emptyWriteSegment))
  })
})
