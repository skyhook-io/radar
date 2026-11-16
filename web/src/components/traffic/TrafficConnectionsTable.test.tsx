// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AggregatedFlow } from '../../types'
import { TrafficConnectionsTable } from './TrafficConnectionsTable'

vi.mock('react-virtuoso', () => ({
  Virtuoso: ({ data, itemContent }: { data: AggregatedFlow[]; itemContent: (index: number, flow: AggregatedFlow) => ReactNode }) =>
    <div>{data.map((flow, index) => <div key={index}>{itemContent(index, flow)}</div>)}</div>,
}))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

const tcp: AggregatedFlow = {
  source: { name: 'client', namespace: 'shop', kind: 'Pod' },
  destination: { name: 'db', namespace: 'data', kind: 'Pod' },
  protocol: 'tcp', port: 5432, connections: 0, bytesSent: 1024, bytesRecv: 2048, flowCount: 1, lastSeen: '',
}

describe('TrafficConnectionsTable', () => {
  it('shows unmeasured request rate and Data for a TCP-only map, and keeps row selection', () => {
    const onSelect = vi.fn()
    const props = { flows: [tcp], isRateBased: true, selection: null, onSelect, onFocus: vi.fn() }
    act(() => root.render(<TrafficConnectionsTable {...props} />))
    const row = container.querySelector<HTMLElement>('[role="row"]')!
    expect(container.textContent).toContain('Req/sData')
    expect(row.textContent).toContain('—3 KB')
    expect(container.textContent).not.toContain('5xx')
    act(() => row.click())
    expect(onSelect).toHaveBeenCalledWith({ type: 'edge', sourceId: 'shop/client', destId: 'data/db', port: 5432, directionUnknown: false })
    act(() => root.render(<TrafficConnectionsTable {...props} selection={onSelect.mock.calls[0][0]} />))
    expect(container.querySelector('[role="row"]')!.getAttribute('aria-selected')).toBe('true')
  })

  it('shows HTTP rate beside bytes-only TCP in a mixed map', () => {
    const http = { ...tcp, destination: { ...tcp.destination, name: 'web' }, port: 80, protocol: 'http', connections: 1, requestRate: 0.25, requestCount: 1 }
    act(() => root.render(<TrafficConnectionsTable flows={[tcp, http]} isRateBased selection={null} onSelect={vi.fn()} onFocus={vi.fn()} />))
    const rows = Array.from(container.querySelectorAll('[role="row"]'))
    expect(rows.find(row => row.textContent?.includes('web'))!.textContent).toContain('0.253 KB')
    expect(rows.find(row => row.textContent?.includes('db'))!.textContent).toContain('—3 KB')
    expect(container.textContent).toContain('5xx')
  })

  it('hides Data when no row has byte figures, and marks a missing row in a mixed view', () => {
    const noBytes = { ...tcp, destination: { ...tcp.destination, name: 'unmeasured' }, bytesSent: 0, bytesRecv: 0 }
    const props = { isRateBased: true, selection: null, onSelect: vi.fn(), onFocus: vi.fn() }
    act(() => root.render(<TrafficConnectionsTable {...props} flows={[noBytes]} />))
    expect(container.textContent).not.toContain('Data')
    expect(container.textContent).not.toContain('0 B')
    act(() => root.render(<TrafficConnectionsTable {...props} flows={[tcp, noBytes]} />))
    const row = Array.from(container.querySelectorAll('[role="row"]')).find(r => r.textContent?.includes('unmeasured'))!
    expect(container.textContent).toContain('Data')
    expect(row.textContent).toContain('——')
    expect(row.textContent).not.toContain('0 B')
  })

  it('preserves connection counts for count-based sources and older TCP rates', () => {
    act(() => root.render(<TrafficConnectionsTable flows={[tcp]} isRateBased={false} selection={null} onSelect={vi.fn()} onFocus={vi.fn()} />))
    expect(container.textContent).toContain('ConnsData')
    expect(container.querySelector('[role="row"]')!.textContent).toContain('03 KB')
    act(() => root.render(<TrafficConnectionsTable flows={[{ ...tcp, connections: 2 }]} isRateBased selection={null} onSelect={vi.fn()} onFocus={vi.fn()} />))
    expect(container.querySelector('[role="row"]')!.textContent).toContain('2.03 KB')
  })
})

describe('TrafficConnectionsTable protocol data', () => {
  it('renders compact protocol figures without their mixed sum', () => {
    const mixed = { ...tcp, bytesSent: 38668, bytesRecv: 652404, bytesByProtocol: {
      http: { bytesSent: 5969, bytesRecv: 260965 }, tcp: { bytesSent: 32699, bytesRecv: 391439 },
    } }
    act(() => root.render(<TrafficConnectionsTable flows={[mixed, tcp]} isRateBased selection={null} onSelect={vi.fn()} onFocus={vi.fn()} />))
    expect(container.textContent).toContain('HTTP 260.7 KB · TCP 414.2 KB')
    const gridClasses = Array.from(container.querySelectorAll('[role="row"]')).map(row => row.className.split(' ').find(c => c.startsWith('grid-cols-')))
    expect(new Set(gridClasses).size).toBe(1)
    expect(gridClasses[0]).toContain('14rem')
    expect(container.firstElementChild!.firstElementChild!.className).toContain(gridClasses[0]!)
    expect(container.textContent).not.toMatch(/674\.9 KB|691,?072/)
  })
})
