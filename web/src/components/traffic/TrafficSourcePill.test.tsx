// @vitest-environment jsdom
import { act, type ComponentProps } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TrafficSourcePill } from './TrafficSourcePill'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type Props = ComponentProps<typeof TrafficSourcePill>

const props = (overrides: Partial<Props>): Props => ({
  sources: ['hubble', 'istio'],
  active: 'istio',
  connection: 'connected',
  onSwitch: vi.fn(),
  onRetry: vi.fn(),
  ...overrides,
})

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

const render = (p: Props) => act(() => root.render(<TrafficSourcePill {...p} />))

const button = (label: string) =>
  Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.trim() === label)

const open = () => act(() => button('istio')!.click())

describe('TrafficSourcePill', () => {
  it('switches source from the picker when connected', () => {
    const p = props({})
    render(p)
    open()
    act(() => button('hubble')!.click())
    expect(p.onSwitch).toHaveBeenCalledWith('hubble')
  })

  it('keeps the picker available when the active source failed to connect', () => {
    const p = props({ connection: 'error' })
    render(p)
    open()
    act(() => button('hubble')!.click())
    expect(p.onSwitch).toHaveBeenCalledWith('hubble')
  })

  it('offers retry next to the picker on error', () => {
    const p = props({ connection: 'error' })
    render(p)
    act(() => button('retry')!.click())
    expect(p.onRetry).toHaveBeenCalledTimes(1)
    expect(button('istio')).toBeDefined()
  })

  it('does not switch when the active source is picked again', () => {
    const p = props({})
    render(p)
    open()
    act(() => container.querySelector<HTMLButtonElement>('[role="option"][aria-selected="true"]')!.click())
    expect(p.onSwitch).not.toHaveBeenCalled()
  })

  it('shows no picker with a single available source', () => {
    render(props({ sources: ['istio'] }))
    expect(container.querySelector('button[aria-haspopup]')).toBeNull()
    expect(container.textContent).toContain('istio')
  })

  it('shows no picker or retry while connecting', () => {
    render(props({ connection: 'connecting' }))
    expect(container.textContent).toContain('Connecting...')
    expect(container.querySelector('button')).toBeNull()
  })

  it('does not reopen the menu after a retry made without a mouse', () => {
    const p = props({ connection: 'error' })
    render(p)
    open()
    expect(container.querySelector('[role="listbox"]')).not.toBeNull()
    act(() => button('retry')!.click())
    render({ ...p, connection: 'connecting' })
    render({ ...p, connection: 'connected' })
    const menu = container.querySelector('[role="listbox"]')
    expect(menu === null || menu.hasAttribute('inert')).toBe(true)
    expect(button('istio')!.getAttribute('aria-expanded')).toBe('false')
  })

  it('makes the menu inert while it animates closed, so its options cannot be activated', () => {
    const p = props({})
    render(p)
    open()
    expect(container.querySelector('[role="listbox"]')!.hasAttribute('inert')).toBe(false)
    act(() => {
      document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })
    const closing = container.querySelector('[role="listbox"]')
    expect(closing === null || closing.hasAttribute('inert')).toBe(true)
  })
})
