import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

vi.mock('../api/client', () => ({
  useCapabilities: () => ({ data: undefined, error: null }),
  useNamespaceCapabilities: vi.fn(),
}))

import { CapabilitiesProvider, useCapabilitiesContext } from './CapabilitiesContext'

function LocalTerminalCapability() {
  return <span>{String(useCapabilitiesContext().localTerminal)}</span>
}

describe('CapabilitiesContext local terminal defaults', () => {
  it('fails closed when a standalone surface has no provider', () => {
    expect(renderToStaticMarkup(<LocalTerminalCapability />)).toBe('<span>false</span>')
  })

  it('preserves the local loading default inside CapabilitiesProvider', () => {
    const markup = renderToStaticMarkup(
      <CapabilitiesProvider>
        <LocalTerminalCapability />
      </CapabilitiesProvider>,
    )

    expect(markup).toBe('<span>true</span>')
  })
})
