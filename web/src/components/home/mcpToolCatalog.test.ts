import { describe, expect, it } from 'vitest'
import { MCP_TOOL_CATALOG } from './mcpToolCatalog'

describe('diagnose catalog copy', () => {
  const description = MCP_TOOL_CATALOG.find((tool) => tool.name === 'diagnose')?.desc

  it('describes a bounded evidence bundle without promising complete sources or a verdict', () => {
    expect(description).toContain('Bounded, point-in-time evidence bundle')
    expect(description).toContain('not an agent run')
    expect(description).toContain('not an authoritative root-cause verdict')
    expect(description).toContain('attempts selected, capped current and previous logs where available')
    expect(description).toContain('a capped warning-event sample')
    expect(description).toContain('only when the evidence establishes one')
    expect(description).not.toContain('One-call root-cause bundle')
  })

  it('documents group-qualified Argo Rollout evidence', () => {
    const diagnose = MCP_TOOL_CATALOG.find((tool) => tool.name === 'diagnose')
    expect(diagnose?.desc).toContain('Argo Rollout')
    expect(diagnose?.params.find((param) => param.arg === 'group')?.desc).toContain('argoproj.io')
  })
})
