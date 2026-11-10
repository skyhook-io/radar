// @vitest-environment jsdom
import { act, type ComponentProps } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TrafficFilterSidebar } from './TrafficFilterSidebar'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type Props = ComponentProps<typeof TrafficFilterSidebar>

const props = (overrides: Partial<Props>): Props => ({
  hideSystem: true, setHideSystem: vi.fn(),
  hideExternal: false, setHideExternal: vi.fn(),
  minConnections: 0, setMinConnections: vi.fn(),
  showNamespaceGroups: true, setShowNamespaceGroups: vi.fn(),
  collapseInternet: true, setCollapseInternet: vi.fn(),
  groupByWorkload: true, setGroupByWorkload: vi.fn(),
  addonMode: 'show', setAddonMode: vi.fn(),
  aggregateExternal: true, setAggregateExternal: vi.fn(),
  detectServices: true, setDetectServices: vi.fn(),
  timeRange: '5m', setTimeRange: vi.fn(),
  l7Protocol: 'all', setL7Protocol: vi.fn(),
  l7Methods: new Set(), onToggleL7Method: vi.fn(),
  l7StatusRanges: new Set(), onToggleL7StatusRange: vi.fn(),
  verdicts: new Set(), onToggleVerdict: vi.fn(),
  dnsPattern: '', setDnsPattern: vi.fn(),
  namespaces: [{ name: 'demo', nodeCount: 3 }],
  hiddenNamespaces: new Set(), onToggleNamespace: vi.fn(),
  ...overrides,
})

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

const button = (label: string) =>
  Array.from(container.querySelectorAll('button')).find((b) => b.textContent === label)

describe('TrafficFilterSidebar verdict filter', () => {
  it('offers the verdicts on L4-only traffic, where there is no L7 section', () => {
    const onToggleVerdict = vi.fn()
    act(() => root.render(
      <TrafficFilterSidebar {...props({ showL7Filters: false, availableVerdicts: ['forwarded', 'dropped'], onToggleVerdict })} />,
    ))

    expect(container.textContent).not.toContain('L7 Filters')
    expect(button('forwarded')).toBeDefined()
    expect(button('error')).toBeUndefined()

    act(() => button('dropped')!.click())
    expect(onToggleVerdict).toHaveBeenCalledWith('dropped')
  })

  it('marks the selected verdict as pressed', () => {
    act(() => root.render(
      <TrafficFilterSidebar {...props({ availableVerdicts: ['forwarded', 'dropped'], verdicts: new Set(['dropped']) })} />,
    ))
    expect(button('dropped')!.getAttribute('aria-pressed')).toBe('true')
    expect(button('forwarded')!.getAttribute('aria-pressed')).toBe('false')
  })

  it('shows no verdict control when there are no verdict choices', () => {
    act(() => root.render(<TrafficFilterSidebar {...props({ showL7Filters: true, availableVerdicts: [] })} />))
    expect(container.textContent).toContain('L7 Filters')
    expect(container.textContent).not.toContain('Verdict')
  })
})
