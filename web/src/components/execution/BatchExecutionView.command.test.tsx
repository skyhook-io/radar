// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CommandFact } from './BatchExecutionView'

vi.mock('../../api/client', () => ({}))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const script = "sh -c echo '{{inputs.parameters.greeting}}, {{inputs.parameters.target}}'\nset -e\nrun-the-export --all"

let root: Root
let host: HTMLDivElement
let scrollHeight = 0

function render(command = script) {
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  act(() => root.render(<CommandFact command={command} />))
  return host.querySelector<HTMLElement>('[data-testid="definition-command"]')!
}
const button = (name: string) => [...host.querySelectorAll('button')].find((b) => b.textContent === name || b.getAttribute('aria-label') === name)

beforeEach(() => {
  scrollHeight = 0
  Object.defineProperty(HTMLElement.prototype, 'scrollHeight', { configurable: true, get: () => scrollHeight })
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => 64 })
})
afterEach(() => {
  act(() => root.unmount())
  host.remove()
  vi.unstubAllGlobals()
})

describe('CommandFact', () => {
  it('shows the whole command on its own line, keeping script line breaks, with no toggle when it fits', () => {
    scrollHeight = 64
    const code = render()
    expect(code.textContent).toBe(script)
    expect(code.className).toContain('whitespace-pre-wrap')
    expect(code.className).toContain('[overflow-wrap:anywhere]')
    expect(button('Show all')).toBeUndefined()
  })

  it('clamps a command taller than the card and expands it in place', () => {
    scrollHeight = 200
    const code = render()
    expect(code.className).toContain('line-clamp-4')
    const toggle = button('Show all')!
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    act(() => toggle.click())
    expect(code.className).not.toContain('line-clamp-4')
    expect(button('Show less')!.getAttribute('aria-expanded')).toBe('true')
    act(() => button('Show less')!.click())
    expect(code.className).toContain('line-clamp-4')
  })

  it('copies the whole command, not the clamped text', async () => {
    scrollHeight = 200
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    render()
    await act(async () => { button('Copy command')!.click() })
    expect(writeText).toHaveBeenCalledWith(script)
    expect(button('Command copied')).toBeDefined()
  })
})
