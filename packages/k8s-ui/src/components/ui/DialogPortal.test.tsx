// @vitest-environment jsdom
import { act, StrictMode, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
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

function pressTab(shiftKey = false) {
  const target = document.activeElement ?? document.body
  const event = new KeyboardEvent('keydown', { key: 'Tab', shiftKey, bubbles: true, cancelable: true })
  target.dispatchEvent(event)
  return event
}

afterEach(async () => {
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
    const elsewhere = document.createElement('input')
    const { render, opener } = await mount(<Dialog open={false} />)
    document.body.appendChild(elsewhere)
    await render(<Dialog open />)
    await render(<Dialog open={false} />)
    // Restore runs in a microtask after the close commit; move focus first.
    elsewhere.focus()
    await act(async () => {})
    expect(document.activeElement).toBe(elsewhere)
    expect(document.activeElement).not.toBe(opener)
  })

  it('wraps Tab and Shift+Tab within the panel', async () => {
    await mount(
      <Dialog open>
        <button>first</button>
        <button disabled>disabled</button>
        <button>last</button>
      </Dialog>,
    )
    const [first, , last] = Array.from(dialog()!.querySelectorAll('button'))
    last.focus()
    expect(pressTab().defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(first)
    expect(pressTab(true).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(last)
    first.focus()
    expect(pressTab().defaultPrevented).toBe(false)
  })

  it('keeps focus on the panel when it has nothing tabbable', async () => {
    await mount(<Dialog open />)
    expect(pressTab().defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(dialog())
  })

  it('leaves Tab alone when a child editor already handled it', async () => {
    await mount(
      <Dialog open>
        <button>first</button>
        <textarea aria-label="yaml" onKeyDown={e => { if (e.key === 'Tab') e.preventDefault() }} />
      </Dialog>,
    )
    const textarea = document.querySelector('textarea')!
    textarea.focus()
    pressTab()
    expect(document.activeElement).toBe(textarea)
  })
})
