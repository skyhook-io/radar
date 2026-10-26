// @vitest-environment jsdom
//
// The hook must leave a caller's own width constraints alone unless the menu
// can't fit, and put them back exactly once it fits again.
import { act, useRef } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it } from 'vitest'
import { useKeepInViewport } from './useKeepInViewport'

let root: Root | undefined
let host: HTMLDivElement | undefined
afterEach(() => {
  act(() => root?.unmount())
  host?.remove()
})

function Menu({ tick }: { tick: number }) {
  const ref = useRef<HTMLDivElement>(null)
  useKeepInViewport(ref, true)
  return <div ref={ref} data-tick={tick} style={{ minWidth: 240 }} />
}

function setMenuWidth(el: HTMLElement, width: number) {
  Object.defineProperty(el, 'offsetWidth', { configurable: true, value: width })
  el.getBoundingClientRect = () => ({ left: 0, right: width, top: 0, bottom: 0, width, height: 0, x: 0, y: 0, toJSON: () => ({}) })
}

function render(tick: number) {
  act(() => root!.render(<Menu tick={tick} />))
  return host!.querySelector('div')!
}

it('keeps the caller min-width when the menu fits, and restores it after a squeeze', () => {
  Object.defineProperty(document.documentElement, 'clientWidth', { configurable: true, value: 1024 })
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  let el = render(1)
  setMenuWidth(el, 260)
  el = render(2)
  expect(el.style.minWidth).toBe('240px')
  expect(el.style.maxWidth).toBe('')

  Object.defineProperty(document.documentElement, 'clientWidth', { configurable: true, value: 200 })
  el = render(3)
  expect(el.style.minWidth).toBe('0px')
  expect(el.style.maxWidth).toBe('184px')

  Object.defineProperty(document.documentElement, 'clientWidth', { configurable: true, value: 1024 })
  el = render(4)
  expect(el.style.minWidth).toBe('240px')
  expect(el.style.maxWidth).toBe('')
})

it('measures with transitions off and restores the caller transition', () => {
  Object.defineProperty(document.documentElement, 'clientWidth', { configurable: true, value: 1024 })
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  let el = render(1)
  el.style.transition = 'opacity 100ms'
  const seen: string[] = []
  el.getBoundingClientRect = () => {
    seen.push(el.style.transition)
    return { left: 410, right: 650, top: 0, bottom: 0, width: 240, height: 0, x: 410, y: 0, toJSON: () => ({}) }
  }
  el = render(2)
  expect(seen.length).toBeGreaterThan(0)
  expect(seen.every((t) => t === 'none')).toBe(true)
  expect(el.style.transition).toBe('opacity 100ms')
  expect(el.style.translate).toBe('')
})
