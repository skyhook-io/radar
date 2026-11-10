import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, expect, it, vi } from 'vitest'
import { OpenTelemetryCollectorRenderer } from './OpenTelemetryCollectorRenderer'

const read = vi.hoisted(() => vi.fn())
vi.mock('../../../api/client', () => ({ useResource: read }))
const root = { apiVersion: 'opentelemetry.io/v1beta1', kind: 'OpenTelemetryCollector', metadata: { name: 'collector', namespace: 'observe', uid: 'root' }, spec: { mode: 'deployment', config: {} } }
const reference = { group: 'apps', kind: 'Deployment', namespace: 'observe', name: 'runtime' }
const workload = { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { namespace: 'observe', ownerReferences: [{ apiVersion: 'opentelemetry.io/v1beta1', kind: 'OpenTelemetryCollector', name: 'collector', uid: 'root', controller: true }] }, spec: { replicas: 0 }, status: {} }

beforeEach(() => read.mockReset())

it('separates current scaled-to-zero workload state from operator readiness', () => {
  read.mockImplementation((kind: string) => kind === 'opentelemetrycollectors' ? { relationships: { children: [reference], ownership: { children: { complete: true } } } } : { data: workload })
  const html = renderToStaticMarkup(<OpenTelemetryCollectorRenderer data={root} />)
  expect(html).toContain('Current Workloads')
  expect(html).toContain('Deployment / runtime')
  expect(html).toContain('Scaled to 0')
  expect(html).not.toContain('Not currently controlled')
})

it('does not attribute a replaced workload to the current Collector', () => {
  const replaced = structuredClone(workload)
  replaced.metadata.ownerReferences[0].uid = 'old-root'
  read.mockImplementation((kind: string) => kind === 'opentelemetrycollectors' ? { relationships: { children: [reference], ownership: { children: { complete: true } } } } : { data: replaced })
  const html = renderToStaticMarkup(<OpenTelemetryCollectorRenderer data={root} />)
  expect(html).toContain('Not currently controlled')
  expect(html).not.toContain('Scaled to 0')
})

it('keeps unknown evidence and sidecar emptiness neutral and explicit', () => {
  read.mockReturnValue({})
  expect(renderToStaticMarkup(<OpenTelemetryCollectorRenderer data={root} />)).toContain('Owned-workload evidence is unavailable')
  read.mockReturnValue({ relationships: { children: [], ownership: { children: { complete: true, narrowed: true } } } })
  const html = renderToStaticMarkup(<OpenTelemetryCollectorRenderer data={{ ...root, spec: { ...root.spec, mode: 'sidecar' } }} />)
  expect(html).toContain('Sidecar mode runs inside annotated application Pods')
  expect(html).toContain('Only resources you can read are shown')
})

it('bounds automatic workload reads while leaving additional objects followable', () => {
  const children = Array.from({ length: 30 }, (_, index) => ({ ...reference, name: `runtime-${index}` }))
  read.mockImplementation((kind: string) => kind === 'opentelemetrycollectors' ? { relationships: { children, ownership: { children: { complete: true } } } } : { data: workload })
  const html = renderToStaticMarkup(<OpenTelemetryCollectorRenderer data={root} />)
  expect(read).toHaveBeenCalledTimes(4)
  expect(html).toContain('This workload preview is incomplete')
})

it('does not warn about workload coverage because unrelated CRD kinds are uncollected', () => {
  read.mockImplementation((kind: string) => kind === 'opentelemetrycollectors' ? { relationships: { children: [reference], ownership: { children: { complete: false, notChecked: [{ group: 'example.io', kind: 'Widget', reason: 'radar_kind_not_collected' }] } } } } : { data: workload })
  const html = renderToStaticMarkup(<OpenTelemetryCollectorRenderer data={root} />)
  expect(html).toContain('Scaled to 0')
  expect(html).not.toContain('preview is incomplete')
})

it('shows an explicit partial workload preview when an apps inventory is unavailable', () => {
  read.mockReturnValue({ relationships: { children: [], ownership: { children: { complete: false, notChecked: [{ group: 'apps', kind: 'DaemonSet', reason: 'kind_sync_pending' }] } } } })
  expect(renderToStaticMarkup(<OpenTelemetryCollectorRenderer data={root} />)).toContain('This workload preview is incomplete')
  read.mockReturnValue({ isLoading: true })
  expect(renderToStaticMarkup(<OpenTelemetryCollectorRenderer data={root} />)).toContain('Reading owned workloads')
})

it('pauses root and child reads while the resource view is inactive', () => {
  read.mockImplementation((kind: string) => kind === 'opentelemetrycollectors' ? { relationships: { children: [reference], ownership: { children: { complete: true } } } } : { data: workload })
  renderToStaticMarkup(<OpenTelemetryCollectorRenderer data={root} active={false} />)
  expect(read).toHaveBeenCalledTimes(2)
  for (const call of read.mock.calls) expect(call[4]).toMatchObject({ enabled: false, refetchInterval: false })
})
