// @vitest-environment jsdom
import { act, StrictMode, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DialogPortal } from './DialogPortal'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const roots = new Set<Root>()

async function mount(node: ReactNode) {
  const opener = document.createElement('button')
  opener.textContent = 'opener'
  document.body.appendChild(opener)
  opener.focus()
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  roots.add(root)
  await act(async () => root.render(node))
  return { root, opener, render: (next: ReactNode) => act(async () => root.render(next)) }
}

const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')
const guards = () => Array.from(document.querySelectorAll<HTMLElement>('[data-focus-guard]'))

function pressTab(shiftKey = false) {
  const target = document.activeElement ?? document.body
  const event = new KeyboardEvent('keydown', { key: 'Tab', shiftKey, bubbles: true, cancelable: true })
  target.dispatchEvent(event)
  return event
}

afterEach(async () => {
  vi.restoreAllMocks()
  for (const root of roots) await act(async () => root.unmount())
  roots.clear()
  document.body.replaceChildren()
})

function Dialog({ open, onClose = () => {}, children }: { open: boolean; onClose?: () => void; children?: ReactNode }) {
  return (
    <DialogPortal open={open} onClose={onClose} ariaLabelledBy="dialog-title">
      <h3 id="dialog-title">Title</h3>
      {children}
    </DialogPortal>
  )
}

describe('DialogPortal accessibility', () => {
  it('moves focus into a dialog that was mounted closed and then opened', async () => {
    const { render } = await mount(<Dialog open={false} />)
    expect(dialog()).toBeNull()
    await render(<Dialog open />)
    expect(document.activeElement).toBe(dialog())
  })

  it('leaves focus on a child that autoFocused', async () => {
    const { render } = await mount(<Dialog open={false} />)
    await render(<Dialog open><input autoFocus aria-label="replicas" /></Dialog>)
    expect(document.activeElement).toBe(document.querySelector('input'))
  })

  it('names the dialog from ariaLabelledBy, or ariaLabel when no title is referenced', async () => {
    await mount(<Dialog open />)
    expect(dialog()?.getAttribute('aria-labelledby')).toBe('dialog-title')
    expect(dialog()?.hasAttribute('aria-label')).toBe(false)
    await act(async () => { for (const root of roots) root.unmount() })
    roots.clear()
    await mount(<DialogPortal open onClose={() => {}} ariaLabel="Scale workload">x</DialogPortal>)
    expect(dialog()?.getAttribute('aria-label')).toBe('Scale workload')
  })

  it('restores focus to the opener at logical close, before the exit finishes', async () => {
    const { render, opener } = await mount(<Dialog open={false} />)
    await render(<Dialog open><button>inside</button></Dialog>)
    expect(document.activeElement).toBe(dialog())
    await render(<Dialog open={false}><button>inside</button></Dialog>)
    expect(dialog()).not.toBeNull()
    expect(dialog()?.closest('[inert]')).not.toBeNull()
    expect(document.activeElement).toBe(opener)
  })

  it('restores focus when a conditionally-mounted dialog unmounts', async () => {
    function Host() {
      const [show, setShow] = useState(true)
      return show ? <Dialog open onClose={() => setShow(false)}><button>inside</button></Dialog> : null
    }
    const { opener } = await mount(<Host />)
    expect(document.activeElement).toBe(dialog())
    await act(async () => {
      dialog()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    })
    expect(dialog()).toBeNull()
    expect(document.activeElement).toBe(opener)
  })

  it('does not steal focus back from an autoFocus child under StrictMode', async () => {
    const { opener } = await mount(
      <StrictMode>
        <Dialog open><input autoFocus aria-label="name" /></Dialog>
      </StrictMode>,
    )
    await act(async () => {})
    expect(document.activeElement).toBe(document.querySelector('input'))
    expect(document.activeElement).not.toBe(opener)
  })

  it('does not pull focus back when the close moved it elsewhere', async () => {
    // The destination takes focus in the same commit as the close — as a
    // navigated-to view's autoFocus does — so the restore microtask sees it.
    const { render, opener } = await mount(<Dialog open={false} />)
    await render(<Dialog open><button>inside</button></Dialog>)
    await render(<><Dialog open={false}><button>inside</button></Dialog><input autoFocus aria-label="destination" /></>)
    const destination = document.querySelector('input[aria-label="destination"]')
    expect(document.activeElement).toBe(destination)
    expect(document.activeElement).not.toBe(opener)
  })

  it('wraps focus that leaves past either edge of the panel', async () => {
    await mount(
      <Dialog open>
        <button>first</button>
        <button disabled>disabled</button>
        <button>last</button>
      </Dialog>,
    )
    const [first, , last] = Array.from(dialog()!.querySelectorAll('button'))
    const [startGuard, endGuard] = guards()
    last.focus()
    endGuard.focus()
    expect(document.activeElement).toBe(first)
    startGuard.focus()
    expect(document.activeElement).toBe(last)
  })

  it('enters at the near edge when focus arrives at a guard from outside', async () => {
    await mount(<Dialog open><button>first</button><button>last</button></Dialog>)
    const [first, last] = Array.from(dialog()!.querySelectorAll('button'))
    const [startGuard, endGuard] = guards()
    ;(document.activeElement as HTMLElement).blur()
    startGuard.focus()
    expect(document.activeElement).toBe(first)
    ;(document.activeElement as HTMLElement).blur()
    endGuard.focus()
    expect(document.activeElement).toBe(last)
  })

  it('keeps focus on the panel when it has nothing tabbable', async () => {
    await mount(<Dialog open />)
    guards()[1].focus()
    expect(document.activeElement).toBe(dialog())
  })

  it('never intercepts Tab inside the panel, leaving the order to the browser', async () => {
    await mount(<Dialog open><button>first</button><button>last</button></Dialog>)
    const [first, last] = Array.from(dialog()!.querySelectorAll('button'))
    last.focus()
    expect(pressTab().defaultPrevented).toBe(false)
    first.focus()
    expect(pressTab(true).defaultPrevented).toBe(false)
  })

  it('restores focus to the opener after an autoFocus child took it, however the dialog mounted', async () => {
    const { render, opener } = await mount(<Dialog open={false} />)
    await render(<Dialog open><input autoFocus aria-label="replicas" /></Dialog>)
    expect(document.activeElement).toBe(document.querySelector('input'))
    await render(<Dialog open={false}><input autoFocus aria-label="replicas" /></Dialog>)
    expect(document.activeElement).toBe(opener)

    function Host() {
      const [show, setShow] = useState(true)
      return show ? <Dialog open onClose={() => setShow(false)}><input autoFocus aria-label="name" /></Dialog> : null
    }
    await act(async () => { for (const root of roots) root.unmount() })
    roots.clear()
    const second = await mount(<Host />)
    expect(document.activeElement).toBe(document.querySelector('input'))
    await act(async () => {
      document.querySelector('input')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    })
    expect(dialog()).toBeNull()
    expect(document.activeElement).toBe(second.opener)
  })

  it('wraps to the visible edges, skipping CSS-hidden controls', async () => {
    // jsdom has no layout; stand in for the browser's visibility check.
    const proto = HTMLElement.prototype
    const original = Object.getOwnPropertyDescriptor(proto, 'checkVisibility')
    proto.checkVisibility = function (this: HTMLElement) { return this.style.display !== 'none' }
    try {
      await mount(
        <Dialog open>
          <button style={{ display: 'none' }}>hidden first</button>
          <button>first</button>
          <button>last</button>
          <button style={{ display: 'none' }}>hidden last</button>
        </Dialog>,
      )
      const [, first, last] = Array.from(dialog()!.querySelectorAll('button'))
      const [startGuard, endGuard] = guards()
      last.focus()
      endGuard.focus()
      expect(document.activeElement).toBe(first)
      startGuard.focus()
      expect(document.activeElement).toBe(last)
    } finally {
      if (original) Object.defineProperty(proto, 'checkVisibility', original)
      else Reflect.deleteProperty(proto, 'checkVisibility')
    }
  })

  it('pulls a Tab back into the dialog after the focused control was disabled', async () => {
    await mount(
      <Dialog open>
        <button>first</button>
        <button>last</button>
      </Dialog>,
    )
    const [first, last] = Array.from(dialog()!.querySelectorAll('button'))
    ;(document.activeElement as HTMLElement).blur()
    expect(document.activeElement).toBe(document.body)
    expect(pressTab().defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(first)
    ;(document.activeElement as HTMLElement).blur()
    expect(pressTab(true).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(last)
  })

  it('leaves Tab alone in a menu portaled out of the dialog', async () => {
    await mount(<Dialog open><button>inside</button></Dialog>)
    const menuItem = document.createElement('button')
    document.body.appendChild(menuItem)
    menuItem.focus()
    expect(pressTab().defaultPrevented).toBe(false)
    expect(document.activeElement).toBe(menuItem)
  })

  it('lets the dialog underneath recover Tab while the one above it animates out', async () => {
    function Stack({ top }: { top: boolean }) {
      return (
        <>
          <Dialog open><button>under</button></Dialog>
          <DialogPortal open={top} onClose={() => {}} ariaLabel="top"><button>over</button></DialogPortal>
        </>
      )
    }
    const { render } = await mount(<Stack top />)
    await render(<Stack top={false} />)
    // The closing dialog is still mounted (inert) for its exit animation.
    expect(document.querySelectorAll('[role="dialog"]').length).toBe(2)
    ;(document.activeElement as HTMLElement | null)?.blur()
    expect(pressTab().defaultPrevented).toBe(true)
    expect(document.activeElement?.textContent).toBe('under')
  })
})
