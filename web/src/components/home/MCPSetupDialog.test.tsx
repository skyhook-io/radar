// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { MCPSetupDialog } from './MCPSetupDialog'
import { mcpEndpointUrl, setBasename } from '../../api/config'
import { MCP_TOOL_CATALOG } from './mcpToolCatalog'

const clipboard = vi.hoisted(() => ({ ok: true, copied: [] as string[] }))
vi.mock('@skyhook-io/k8s-ui/utils/clipboard', () => ({
  copyText: async (text: string) => {
    if (clipboard.ok) clipboard.copied.push(text)
    return clipboard.ok
  },
}))

describe('MCP setup in read-only mode', () => {
  it('generates client configurations for the retained endpoint and hides writes', () => {
    setBasename('/radar')
    try {
      const url = mcpEndpointUrl(true)
      const html = renderToStaticMarkup(<MCPSetupDialog open readOnly mcpUrl={url} onClose={() => {}} />)
      expect(html).toContain(`claude mcp add --transport http radar ${url}`)
      expect(html).toContain(`codex mcp add radar --url ${url}`)
      expect(html).toContain('/radar/mcp-readonly')
      expect(html).toContain('list_namespaces')
      expect(html).not.toContain('apply_resource')
      expect(html).not.toContain('exec_pod')
      expect(html).not.toContain('in_cluster')
      expect(html).toContain('Read-only mode blocks write tools')
    } finally {
      setBasename('')
    }
  })

  it('explains disabled exec without including it in the available tool count', () => {
    const html = renderToStaticMarkup(<MCPSetupDialog open execTools={false} mcpUrl={mcpEndpointUrl()} onClose={() => {}} />)
    expect(html).toContain('apply_resource')
    expect(html).toContain('in_cluster')
    expect(html).toContain('exec_pod')
    expect(html).toContain('is disabled')
    expect(html).toContain('--mcp-exec-tools')
    expect(html).toContain(`${MCP_TOOL_CATALOG.length - 1} tools`)
  })

  it('does not suggest an unsupported flag when the capability is absent', () => {
    const html = renderToStaticMarkup(<MCPSetupDialog open mcpUrl={mcpEndpointUrl()} onClose={() => {}} />)
    expect(html).not.toContain('exec_pod')
    expect(html).not.toContain('--mcp-exec-tools')
  })

  it('shows exec and counts it only when enabled', () => {
    const html = renderToStaticMarkup(<MCPSetupDialog open execTools mcpUrl={mcpEndpointUrl()} onClose={() => {}} />)
    expect(html).toContain('exec_pod')
    expect(html).toContain(`${MCP_TOOL_CATALOG.length} tools`)
    expect(html.indexOf('exec_pod')).toBeGreaterThan(html.indexOf('get_workload_logs'))
  })

  it('hides exec in read-only mode even if exec is requested', () => {
    const html = renderToStaticMarkup(<MCPSetupDialog open readOnly execTools mcpUrl={mcpEndpointUrl(true)} onClose={() => {}} />)
    expect(html).not.toContain('exec_pod')
    expect(html).toContain(`${MCP_TOOL_CATALOG.filter((tool) => !tool.write).length} tools`)
  })
})

describe('MCP setup prompt tip', () => {
  it('tips to name Radar under the client setups, as plain text, without the in-Radar link when no handler is given', () => {
    const host = document.createElement('div')
    host.innerHTML = renderToStaticMarkup(<MCPSetupDialog open mcpUrl={mcpEndpointUrl()} onClose={() => {}} />)
    const tip = [...host.querySelectorAll('p')].find((p) => p.textContent?.startsWith('Tip:'))
    expect(tip?.textContent).toBe(
      "Tip: name Radar in your prompt so your agent uses Radar instead of kubectl, e.g. Use Radar to find what's broken in my cluster and why.",
    )
    expect(tip!.querySelector('code.inline-code')?.textContent).toBe("Use Radar to find what's broken in my cluster and why.")
    expect(tip!.querySelector('button, pre')).toBeNull()
    expect(tip!.nextElementSibling).toBeNull()
    const html = host.innerHTML
    expect(html.indexOf('Tip:')).toBeGreaterThan(html.indexOf('Connect your AI tool'))
    expect(html.indexOf('Tip:')).toBeLessThan(html.indexOf('list_namespaces'))
    expect(html).not.toContain('investigate from Radar itself')
  })

  it('closes the dialog, then opens the composer from the in-Radar link', () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    const calls: string[] = []
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    try {
      act(() => root.render(
        <MCPSetupDialog
          open
          mcpUrl={mcpEndpointUrl()}
          onClose={() => calls.push('close')}
          onInvestigate={() => calls.push('investigate')}
        />,
      ))
      const link = [...host.querySelectorAll('button')].find((b) => /investigate from Radar itself/.test(b.textContent ?? ''))
      expect(link).toBeTruthy()
      act(() => link!.click())
      expect(calls).toEqual(['close', 'investigate'])
    } finally {
      act(() => root.unmount())
      host.remove()
    }
  })

  it.each([
    [true, 'Copied'],
    [false, 'Copy to clipboard'],
  ])('copies the endpoint and shows the tick only when the copy worked (ok=%s)', async (ok, label) => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    clipboard.ok = ok
    clipboard.copied = []
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    try {
      act(() => root.render(<MCPSetupDialog open mcpUrl={mcpEndpointUrl()} onClose={() => {}} />))
      const endpoint = [...host.querySelectorAll('code')].find((c) => c.textContent === mcpEndpointUrl())!
      const copy = endpoint.closest('.relative')!.querySelector('button')!
      await act(async () => copy.click())
      expect(clipboard.copied).toEqual(ok ? [mcpEndpointUrl()] : [])
      expect(copy.getAttribute('aria-label')).toBe(label)
    } finally {
      act(() => root.unmount())
      host.remove()
    }
  })
})
