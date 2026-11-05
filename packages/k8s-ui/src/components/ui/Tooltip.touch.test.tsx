// @vitest-environment jsdom
//
// A tap fires mouseenter and focus. On an enabled control the tooltip must not
// pop over what the tap opens; on a disabled control the tap is how a touch
// user reads why it's disabled.
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { Tooltip } from './Tooltip'
import { RowActionMenu } from './RowActionMenu'
import { RefreshCw } from 'lucide-react'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

let root: Root | undefined
let host: HTMLDivElement | undefined
afterEach(() => {
  act(() => root?.unmount())
  host?.remove()
  vi.useRealTimers()
  // The last touch time is module state; a key press clears it for the next file.
  document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }))
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

it.each(['button', 'input', 'select', 'textarea', 'a'] as const)('explains an aria-disabled %s on touch focus and click', tag => {
  const control = tag === 'button' ? <button aria-disabled="true">Sync</button>
    : tag === 'input' ? <input aria-disabled="true" />
    : tag === 'select' ? <select aria-disabled="true" />
    : tag === 'textarea' ? <textarea aria-disabled="true" />
    : <a href="#" aria-disabled="true">Sync</a>
  const trigger = render(control)
  const child = host!.querySelector(tag)!
  touchPress()
  act(() => trigger.dispatchEvent(new Event('pointerdown', { bubbles: true })))
  hover(trigger)
  act(() => child.dispatchEvent(new FocusEvent('focusin', { bubbles: true })))
  act(() => child.dispatchEvent(new MouseEvent('click', { bubbles: true })))
  expect(tooltipShown()).toBe(true)
})

it('describes a denied row action before focus and explains a touch attempt without running it', () => {
  const onClick = vi.fn()
  const reason = "Your role can't patch Argo CD Application demo in argocd."
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  act(() => root!.render(<RowActionMenu items={[
    { key: 'sync', label: 'Sync', icon: RefreshCw, onClick, disabled: true, disabledReason: reason },
    { key: 'refresh', label: 'Refresh', icon: RefreshCw, onClick },
  ]} />))
  act(() => host!.querySelector('button')!.click())
  const button = host.querySelector<HTMLButtonElement>('[role="menuitem"]')!
  const descriptionId = button.getAttribute('aria-describedby')!
  expect(document.getElementById(descriptionId)?.textContent).toBe(reason)
  expect(document.getElementById(descriptionId)?.className).toBe('sr-only')
  expect(document.querySelector('[role="tooltip"]')).toBeNull()
  expect(button.disabled).toBe(false)
  expect(host.querySelectorAll('[role="menuitem"]')[1].hasAttribute('aria-describedby')).toBe(false)
  touchPress()
  act(() => button.dispatchEvent(new Event('pointerdown', { bubbles: true })))
  hover(button.parentElement!)
  act(() => button.focus())
  act(() => button.click())
  act(() => { vi.advanceTimersByTime(310) })
  expect(document.querySelector('[role="tooltip"]')?.textContent?.trim()).toBe(reason)
  expect(onClick).not.toHaveBeenCalled()
  expect(host.querySelector('[role="menu"]')).not.toBeNull()
})
