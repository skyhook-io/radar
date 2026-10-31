// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it } from 'vitest'
import { SearchSyntaxHelp } from './SearchSyntaxHelp'

let root: Root | undefined
let host: HTMLDivElement | undefined
afterEach(() => {
  act(() => root?.unmount())
  host?.remove()
})

function render(eventContentSearch = false) {
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  act(() => root!.render(<SearchSyntaxHelp eventContentSearch={eventContentSearch} />))
  return host.querySelector('button')!
}

const cardShown = () => document.body.textContent?.includes('Search syntax') ?? false

it.each([false, true])('describes only Event search supported by the server: %s', (supported) => {
  const button = render(supported)
  act(() => button.click())
  expect(document.body.textContent).toContain(supported ? 'search Event reasons and messages' : 'search Events by name')
  expect(document.body.textContent).not.toContain(supported ? 'search Events by name' : 'search Event reasons and messages')
})

it('toggles on a click with no pointer before it (assistive activation)', () => {
  const button = render()
  act(() => button.click())
  expect(cardShown()).toBe(true)
  expect(button.getAttribute('aria-expanded')).toBe('true')
  act(() => button.click())
  expect(cardShown()).toBe(false)
})

it('keeps a hover-opened card open on the click that follows', () => {
  const button = render()
  act(() => { button.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, relatedTarget: document.body })) })
  expect(cardShown()).toBe(true)
  act(() => button.click())
  expect(cardShown()).toBe(true)
})
