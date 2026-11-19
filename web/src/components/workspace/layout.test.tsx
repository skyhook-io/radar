// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { Mono, SectionTable } from './layout'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('uses normal word boundaries with overflow wrapping and keeps the complete value in a hover tooltip', () => {
  vi.useFakeTimers()
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  const image = 'ghcr.io/cloudnative-pg/postgresql:17.4-standard-bookworm'
  act(() => root.render(<Mono>{image}</Mono>))
  const mono = host.querySelector('.font-mono')!
  expect(mono.classList.contains('break-all')).toBe(false)
  expect(mono.classList.contains('break-normal')).toBe(true)
  expect(mono.textContent).toBe(image)
  expect(mono.classList.contains('[overflow-wrap:anywhere]')).toBe(true)
  const trigger = mono.parentElement!
  act(() => trigger.dispatchEvent(new MouseEvent('mouseenter', { bubbles: true })))
  act(() => trigger.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
  act(() => vi.advanceTimersByTime(500))
  expect(document.body.querySelector('.fixed')?.textContent).toBe(image)
  act(() => root.unmount())
  host.remove()
  vi.useRealTimers()
})

it('opens a row from the keyboard the way a click does, and leaves keys on controls inside it alone', () => {
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  const onInspect = vi.fn()
  const inner = vi.fn()
  act(() => root.render(
    <SectionTable
      title="Things"
      columns={[{ header: 'Name', cell: (r: { name: string }) => <button type="button" onClick={inner}>{r.name}</button> }]}
      rows={[{ name: 'a' }]}
      rowKey={(r) => r.name}
      rowResource={(r) => ({ kind: 'Schedule', group: 'velero.io', namespace: 'velero', name: r.name })}
      onInspect={onInspect}
      inspected={null}
      empty="none"
    />,
  ))
  const row = host.querySelector('tbody tr') as HTMLTableRowElement
  expect(row.tabIndex).toBe(0)
  act(() => row.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
  expect(onInspect).toHaveBeenCalledTimes(1)
  act(() => host.querySelector('tbody button')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
  expect(onInspect).toHaveBeenCalledTimes(1)
  act(() => root.unmount())
  host.remove()
})
