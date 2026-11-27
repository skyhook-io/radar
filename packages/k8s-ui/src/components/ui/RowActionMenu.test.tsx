// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Download } from 'lucide-react'
import { RowActionMenu } from './RowActionMenu'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

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

function Menu({ onAction }: { onAction: () => void }) {
  const [shown, setShown] = useState(false)
  return (
    <RowActionMenu
      items={[
        { key: 'download', label: 'Download', icon: Download, onClick: onAction },
        { key: 'show', label: 'Show Published', checked: shown, onClick: () => setShown(!shown), divider: true },
      ]}
    />
  )
}

const trigger = () => container.querySelector<HTMLButtonElement>('button[aria-haspopup="menu"]')!
const item = (label: string) => Array.from(container.querySelectorAll<HTMLButtonElement>('[role^="menuitem"]')).find((b) => b.textContent === label)

describe('RowActionMenu', () => {
  it('keeps the menu open when a checkbox item is clicked, and closes it for an action', () => {
    const onAction = vi.fn()
    act(() => root.render(<Menu onAction={onAction} />))
    act(() => trigger().click())
    expect(trigger().getAttribute('aria-expanded')).toBe('true')

    const show = item('Show Published')!
    expect(show.getAttribute('role')).toBe('menuitemcheckbox')
    expect(show.getAttribute('aria-checked')).toBe('false')
    act(() => show.click())
    expect(trigger().getAttribute('aria-expanded')).toBe('true')
    expect(item('Show Published')!.getAttribute('aria-checked')).toBe('true')

    act(() => item('Download')!.click())
    expect(onAction).toHaveBeenCalledOnce()
    expect(trigger().getAttribute('aria-expanded')).toBe('false')
  })
})
