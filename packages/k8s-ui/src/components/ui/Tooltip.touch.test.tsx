// @vitest-environment jsdom
//
// A tap fires mouseenter and focus. On an enabled control the tooltip must not
// pop over what the tap opens; on a disabled control the tap is how a touch
// user reads why it's disabled.
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { Tooltip } from './Tooltip'

let root: Root | undefined
let host: HTMLDivElement | undefined
afterEach(() => {
  act(() => root?.unmount())
  host?.remove()
  vi.useRealTimers()
})

function render(child: React.ReactNode) {
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  act(() => root!.render(<Tooltip content="Why" delay={0}>{child}</Tooltip>))
  return host.querySelector('span')!
}

function touchPress() {
  const e = new Event('pointerdown', { bubbles: true }) as Event & { pointerType: string }
  Object.defineProperty(e, 'pointerType', { value: 'touch' })
  document.body.dispatchEvent(e)
}

function hover(trigger: Element) {
  vi.useFakeTimers()
  act(() => { trigger.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, relatedTarget: document.body })) })
  act(() => { vi.advanceTimersByTime(10) })
}

const tooltipShown = () => document.body.textContent?.includes('Why') ?? false

it('stays closed when a tap activates an enabled control', () => {
  const trigger = render(<button type="button">Open</button>)
  touchPress()
  hover(trigger)
  expect(tooltipShown()).toBe(false)
})

it('still explains a disabled control on tap', () => {
  const trigger = render(<button type="button" disabled>Restart</button>)
  touchPress()
  hover(trigger)
  expect(tooltipShown()).toBe(true)
})
