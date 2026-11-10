// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { AreaChart } from './AreaChart'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root, host: HTMLDivElement
const series = [{ labels: {}, dataPoints: [{ timestamp: 1000, value: 1 }, { timestamp: 1600, value: 2 }] }]
beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.stubGlobal('requestAnimationFrame', () => 0)
  Object.defineProperty(SVGElement.prototype, 'getScreenCTM', { configurable: true, value: () => ({ a: 2, e: 10 }) })
  Object.defineProperty(SVGElement.prototype, 'setPointerCapture', { configurable: true, value: vi.fn() })
  Object.defineProperty(SVGElement.prototype, 'hasPointerCapture', { configurable: true, value: () => true })
  Object.defineProperty(SVGElement.prototype, 'releasePointerCapture', { configurable: true, value: vi.fn() })
  host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host)
})
afterEach(async () => { await act(async () => root.unmount()); host.remove(); vi.unstubAllGlobals() })
async function render(props: Partial<Parameters<typeof AreaChart>[0]> = {}) {
  await act(async () => root.render(<AreaChart series={series} color="currentColor" fillColor="currentColor" unit="count" {...props} />))
  return host.querySelector<SVGRectElement>('rect[fill="transparent"]')!
}
async function pointer(plot: SVGRectElement, type: string, x: number, button = 0, pointerType = 'mouse') {
  await act(async () => {
    const event = new MouseEvent(type, { bubbles: true, clientX: x * 2 + 10, button })
    Object.defineProperty(event, 'pointerId', { value: 1 })
    Object.defineProperty(event, 'pointerType', { value: pointerType })
    plot.dispatchEvent(event)
  })
}
it.each([[84, 960], [960, 84]])('uses the release position and orders forward/reverse ranges (%s,%s)', async (from, to) => {
  const selected = vi.fn(); const plot = await render({ onSelectRange: selected })
  await pointer(plot, 'pointerdown', from); await pointer(plot, 'pointerup', to)
  expect(selected).toHaveBeenCalledWith({ start: 1000, end: 1600 })
})
it('clamps drag release beyond the plot without canceling and clears canceled gestures', async () => {
  const selected = vi.fn(); const plot = await render({ onSelectRange: selected })
  await pointer(plot, 'pointerdown', 84); await pointer(plot, 'pointermove', 1500); await pointer(plot, 'pointerup', 1500)
  expect(selected).toHaveBeenLastCalledWith({ start: 1000, end: 1600 })
  selected.mockClear()
  await pointer(plot, 'pointerdown', 200); await pointer(plot, 'pointercancel', 800); await pointer(plot, 'pointerup', 800)
  expect(selected).not.toHaveBeenCalled(); expect(host.querySelector('[data-chart-selection]')).toBeNull()
})
it('selects a bounded evaluation interval by click and keyboard', async () => {
  const selected = vi.fn(); const plot = await render({ onSelectRange: selected, stepSeconds: 60 })
  await pointer(plot, 'pointerdown', 84); await pointer(plot, 'pointerup', 84)
  expect(selected).toHaveBeenLastCalledWith({ start: 1000, end: 1030 })
  await act(async () => { plot.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'ArrowRight' })) })
  await act(async () => { plot.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'Enter' })) })
  expect(selected).toHaveBeenLastCalledWith({ start: 1030, end: 1090 })
})
it('keeps default hover and exposes a gap reason even beside a nearby sample', async () => {
  const plot = await render({ shadedRanges: [{ start: 1000, end: 1100, label: 'Metrics unavailable' }], stepSeconds: 600 })
  expect(plot.style.cursor).toBe('crosshair'); expect(plot.getAttribute('tabindex')).toBeNull()
  await pointer(plot, 'pointermove', 84)
  expect(host.textContent).toContain('Metrics unavailable'); expect(host.textContent).toContain('1')
})
it('does not treat secondary pointer buttons as a selection', async () => {
  const selected = vi.fn(); const plot = await render({ onSelectRange: selected })
  await pointer(plot, 'pointerdown', 84, 2); await pointer(plot, 'pointerup', 960, 2)
  expect(selected).not.toHaveBeenCalled()
})

it('shows keyboard focus and clears its tooltip after blur or Escape', async () => {
  const plot = await render({ onSelectRange: vi.fn(), stepSeconds: 60 })
  await act(async () => plot.dispatchEvent(new FocusEvent('focusin', { bubbles: true })))
  expect(plot.getAttribute('stroke')).toBe('currentColor')
  await act(async () => plot.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'ArrowRight' })))
  expect(host.querySelector('.pointer-events-none.absolute')).not.toBeNull()
  await act(async () => plot.dispatchEvent(new FocusEvent('focusout', { bubbles: true })))
  expect(plot.getAttribute('stroke')).toBeNull()
  expect(host.querySelector('.pointer-events-none.absolute')).toBeNull()
  await act(async () => plot.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'ArrowRight' })))
  await act(async () => plot.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'Escape' })))
  expect(host.querySelector('.pointer-events-none.absolute')).toBeNull()
})

it('retains a default-chart touch tooltip after a tap without pointermove, then clears on mouse leave', async () => {
  const plot = await render()
  await pointer(plot, 'pointerdown', 84, 0, 'touch')
  await pointer(plot, 'pointerup', 84, 0, 'touch')
  await pointer(plot, 'pointerout', 84, 0, 'touch')
  expect(host.querySelector('.pointer-events-none.absolute')).not.toBeNull()
  expect(host.textContent).toContain('1')
  await act(async () => plot.dispatchEvent(new MouseEvent('mouseout', { bubbles: true })))
  expect(host.querySelector('.pointer-events-none.absolute')).toBeNull()
})

it('paints selected, shaded and dragged ranges above opaque fills and below chart lines', async () => {
  const plot = await render({ fillColor: '#123456', selection: { start: 1000, end: 1100 }, shadedRanges: [{ start: 1100, end: 1200 }], onSelectRange: vi.fn() })
  await pointer(plot, 'pointerdown', 84)
  await pointer(plot, 'pointermove', 500)
  const fill = host.querySelector('path[fill="#123456"]')!
  const line = host.querySelector('path[fill="none"]')!
  const bands = [...host.querySelectorAll('[data-chart-gap], [data-chart-selection], rect[opacity="0.2"]')]
  expect(bands).toHaveLength(3)
  for (const band of bands) {
    expect(fill.compareDocumentPosition(band) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(band.compareDocumentPosition(line) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  }
})

it('does not render a selectable empty chart without a finite timestamp domain', async () => {
  const selected = vi.fn()
  await render({ series: [{ labels: {}, dataPoints: [] }], onSelectRange: selected, stepSeconds: 60 })
  expect(host.querySelector('svg')).toBeNull()
  expect(host.querySelector('[role="button"]')).toBeNull()
  expect(selected).not.toHaveBeenCalled()
})

it('keeps an empty chart selectable when an explicit valid domain supplies its bounds', async () => {
  const selected = vi.fn()
  const plot = await render({ series: [{ labels: {}, dataPoints: [] }], domain: { start: 1000, end: 1600 }, onSelectRange: selected, stepSeconds: 60 })
  await pointer(plot, 'pointerdown', 84)
  await pointer(plot, 'pointerup', 960)
  expect(selected).toHaveBeenCalledWith({ start: 1000, end: 1600 })
})
