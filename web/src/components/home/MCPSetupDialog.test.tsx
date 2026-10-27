// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { MCPSetupDialog } from './MCPSetupDialog'
import { mcpEndpointUrl, setBasename } from '../../api/config'
import { MCP_TOOL_CATALOG } from './mcpToolCatalog'

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
