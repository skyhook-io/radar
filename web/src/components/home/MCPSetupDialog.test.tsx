// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { MCPSetupDialog } from './MCPSetupDialog'
import { mcpEndpointUrl, setBasename } from '../../api/config'

describe('MCP setup in read-only mode', () => {
  it('generates client configurations for the retained endpoint and hides writes', () => {
    setBasename('/radar')
    try {
      const url = mcpEndpointUrl(true)
      const html = renderToStaticMarkup(<MCPSetupDialog open readOnly applicationEvidenceAvailable mcpUrl={url} onClose={() => {}} />)
      expect(html).toContain(`claude mcp add --transport http radar ${url}`)
      expect(html).toContain(`codex mcp add radar --url ${url}`)
      expect(html).toContain('/radar/mcp-readonly')
      expect(html).toContain('list_namespaces')
      expect(html).not.toContain('apply_resource')
      expect(html).not.toContain('collect_application_evidence')
      expect(html).not.toContain('in_cluster')
      expect(html).toContain('Read-only mode blocks write tools')
    } finally {
      setBasename('')
    }
  })

  it('keeps the full catalog in default mode', () => {
    const html = renderToStaticMarkup(<MCPSetupDialog open applicationEvidenceAvailable mcpUrl={mcpEndpointUrl()} onClose={() => {}} />)
    expect(html).toContain('apply_resource')
    expect(html).toContain('in_cluster')
    expect(html).toContain('collect_application_evidence')
  })

  it('hides local-only tools unless the server advertises availability', () => {
    const html = renderToStaticMarkup(<MCPSetupDialog open mcpUrl={mcpEndpointUrl()} onClose={() => {}} />)
    expect(html).toContain('apply_resource')
    expect(html).not.toContain('collect_application_evidence')
  })
})
