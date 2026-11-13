// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { TerminalClipboardToolbar } from './TerminalClipboardToolbar'
import { TerminalPreferencesProvider } from './terminalPreferences'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

it('shows the preference only for hosts that provide it and disables Copy when clipboard access is unavailable', async () => {
  const descriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn() } })
  const element = document.createElement('div')
  const root = createRoot(element)
  try {
    await act(async () => root.render(<TerminalClipboardToolbar hasSelection onCopy={vi.fn()} />))
    expect(element.querySelector('[aria-label="Terminal options"]')).toBeNull()
    await act(async () => root.render(<TerminalPreferencesProvider scope="toolbar-proof"><TerminalClipboardToolbar hasSelection onCopy={vi.fn()} /></TerminalPreferencesProvider>))
    expect(element.querySelector('[aria-label="Terminal options"]')).not.toBeNull()
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
    await act(async () => root.render(<TerminalClipboardToolbar hasSelection onCopy={vi.fn()} />))
    const copy = element.querySelector('button')
    expect(copy?.disabled).toBe(true)
    expect(copy?.title).toBe('Clipboard access requires HTTPS or localhost')
  } finally {
    await act(async () => root.unmount())
    if (descriptor) Object.defineProperty(navigator, 'clipboard', descriptor)
    else Reflect.deleteProperty(navigator, 'clipboard')
  }
})
