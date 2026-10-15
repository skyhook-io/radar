// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { Mono } from './layout'

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
