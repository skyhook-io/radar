import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { OperationalIssuesShownContext } from '../../ui/drawer-components'
import { KagentAgentRenderer } from './KagentAgentRenderer'

const cond = (type: string, status: string, reason: string, message = '') => ({ type, status, reason, message, observedGeneration: 1 })

function agent(spec: any, conditions: any[], status: any = {}) {
  return {
    apiVersion: 'api.kagent.dev/v1alpha3', kind: 'Agent',
    metadata: { name: 'triage', namespace: 'agents', generation: 1 },
    spec: { harnessRef: { name: 'default' }, ...spec },
    status: { observedGeneration: 1, conditions, ...status },
  }
}

const render = (node: any) => renderToStaticMarkup(node).replace(/<!-- -->/g, '')

describe('KagentAgentRenderer', () => {
  it('leads a blocked change with kagent’s words and the revision new sessions still use', () => {
    const html = render(<KagentAgentRenderer data={agent({ template: { ref: { name: 'base' } } }, [
      cond('ResolvedRefs', 'False', 'ReferenceResolutionFailed', 'resolve ModelConfig "agents/gpt": not found'),
      cond('Compatible', 'False', 'Blocked', 'blocked by ResolvedRefs'),
      cond('Ready', 'False', 'Blocked', 'blocked by ResolvedRefs'),
    ], { latestSuccessfulRevision: 'a1b2c3d4e5f6a1b2c3d4e5f6' })} />)
    expect(html).toContain('Latest change blocked')
    expect(html).toContain('resolve ModelConfig &quot;agents/gpt&quot;: not found; new sessions still start on revision a1b2c3d4e5f6')
    expect(html).toContain('Not compiled yet')
    expect(html).toContain('Not visible to Radar')
    // Nothing read the template or the Harness: their rows say so.
    expect(html).toContain('AgentTemplate not read')
    expect(html).toContain('From the AgentTemplate, not read')
    expect(html).toContain('Harness not read')
  })

  it('shows what the template and Harness declare, bounded, and the egress grants kagent did not apply', () => {
    const servers = Array.from({ length: 10 }, (_, i) => ({ mcp: { server: { kind: 'RemoteMCPServer', name: `mcp-${i}` }, tools: i === 0 ? ['a', 'b', 'c', 'd'] : [] } }))
    const html = render(<KagentAgentRenderer
      data={agent({ template: { ref: { name: 'base' } }, egressPolicyRefs: [{ name: 'github' }] }, [
        cond('EgressPoliciesApplied', 'False', 'EgressPolicyNotFound', 'agents/github: not found'),
        cond('Ready', 'True', 'Ready'),
      ], { latestSuccessfulRevision: 'a1b2c3d4e5f6', desiredRevision: 'a1b2c3d4e5f6' })}
      related={{
        template: { state: 'read', object: { spec: { modelConfig: { name: 'gpt', namespace: 'shared' }, tools: [...servers, { subAgent: { name: 'reviewer', templateRef: { name: 'reviewer' } } }] } } },
        harness: { state: 'read', object: { metadata: { namespace: 'agents' }, spec: { kagent: { memory: { modelConfigRef: { name: 'embed' } } }, substrate: { workerPoolRef: { name: 'pool' } } } } },
      }}
      sessions={<div>sessions-slot</div>}
    />)
    expect(html).toContain('Egress not applied')
    expect(html).toContain('agents/github: not found')
    expect(html).toContain('shared/gpt')
    expect(html).toContain('agents/embed')
    expect(html).toContain('pool')
    expect(html).toContain('a, b, c +1 more')
    expect(html).toContain('+2 more')
    expect(html).toContain('reviewer')
    expect(html).toContain('(the same)')
    expect(html).toContain('sessions-slot')
  })

  it('says a referenced object could not be read, and why', () => {
    const html = render(<KagentAgentRenderer
      data={agent({ template: { ref: { name: 'base' } } }, [cond('Ready', 'True', 'Ready')])}
      related={{ template: { state: 'denied' }, harness: { state: 'notFound' } }}
    />)
    expect(html).toContain('You can’t read this AgentTemplate')
    expect(html).toContain('Harness not found')
  })

  it('does not link a model reference to another API group’s kind', () => {
    const html = render(<KagentAgentRenderer
      data={agent({ template: { inline: { modelConfig: { name: 'llm', group: 'models.example.com', kind: 'Model' } } } }, [cond('Ready', 'True', 'Ready')])}
      onNavigate={() => {}}
    />)
    expect(html).toContain('(Model, models.example.com)')
  })

  it('shows each MCP server’s own status, since kagent drops a failed server’s tools and still reads Ready', () => {
    const server = (name: string, conditions: any[], tools: string[] = []) => ({ state: 'read' as const, object: { apiVersion: 'api.kagent.dev/v1alpha3', kind: 'RemoteMCPServer', metadata: { name, namespace: 'agents', generation: 1 }, status: { observedGeneration: 1, conditions, discoveredTools: tools.map(t => ({ name: t, description: t })) } } })
    const html = render(<KagentAgentRenderer
      data={agent({ template: { inline: { tools: [
        { mcp: { server: { kind: 'RemoteMCPServer', name: 'down' } } },
        { mcp: { server: { kind: 'RemoteMCPServer', name: 'github' }, tools: ['list_issues', 'no_such_tool'] } },
        { mcp: { server: { kind: 'RemoteMCPServer', name: 'secret' } } },
      ] } } }, [cond('Ready', 'True', 'Ready')])}
      related={{ mcpServers: {
        'agents/down': server('down', [cond('Accepted', 'False', 'DiscoveryFailed', 'no such host')]),
        'agents/github': server('github', [cond('Accepted', 'True', 'DiscoverySucceeded')], ['list_issues']),
        'agents/secret': { state: 'denied' },
      } }}
    />)
    expect(html).toContain('kagent&#x27;s controller could not discover MCP server agents/down&#x27;s tools')
    expect(html).toContain('omits a server&#x27;s tools when that fails')
    expect(html).toContain('even though the agent reads Ready')
    expect(html).not.toContain('runs without them')
    expect(html).toContain('Discovery failed')
    expect(html).toContain('Not among the tools kagent&#x27;s controller discovered: no_such_tool')
    expect(html).toContain('You can’t read this RemoteMCPServer')
  })

  it('reads the ModelConfigs it names: a status badge, and a missing one named, not linked', () => {
    const model = (conditions: any[]) => ({ state: 'read' as const, object: { apiVersion: 'api.kagent.dev/v1alpha3', kind: 'ModelConfig', metadata: { name: 'gpt', namespace: 'agents', generation: 1 }, status: { observedGeneration: 1, conditions } } })
    const html = render(<KagentAgentRenderer
      data={agent({ template: { inline: { modelConfig: { name: 'gpt' } } } }, [cond('Ready', 'True', 'Ready')])}
      related={{ modelConfigs: { 'agents/gpt': model([cond('Accepted', 'True', 'Accepted'), cond('ResolvedRefs', 'False', 'APIKeySecretNotFound', 'secret openai not found')]) } }}
      onNavigate={() => {}}
    />)
    expect(html).toContain('Blocked')
    const missing = render(<KagentAgentRenderer
      data={agent({ template: { inline: { modelConfig: { name: 'no-such-model' } } } }, [cond('Ready', 'True', 'Ready')])}
      related={{ modelConfigs: { 'agents/no-such-model': { state: 'notFound' } } }}
      onNavigate={() => {}}
    />)
    expect(missing).toContain('agents/no-such-model<span class="text-theme-text-tertiary"> · not found')
    expect(missing).not.toMatch(/<button[^>]*>agents\/no-such-model<\/button>/)
  })

  it('leaves a reading the Issues card states to that card, and shows its Reading row only when Ready', () => {
    const blocked = agent({ template: { ref: { name: 'base' } } }, [
      cond('ResolvedRefs', 'False', 'ReferenceResolutionFailed', 'resolve ModelConfig "agents/gpt": not found'),
      cond('Ready', 'False', 'Blocked', 'blocked by ResolvedRefs'),
    ])
    const withIssues = render(<OperationalIssuesShownContext.Provider value={true}><KagentAgentRenderer data={blocked} /></OperationalIssuesShownContext.Provider>)
    expect(withIssues).not.toContain('Latest change blocked')
    expect(withIssues).not.toContain('>Reading<')
    expect(render(<KagentAgentRenderer data={blocked} />)).toContain('Latest change blocked')
    expect(render(<KagentAgentRenderer data={agent({ template: { ref: { name: 'base' } } }, [cond('Ready', 'True', 'Ready')])} />)).toContain('Ready to start sessions')
  })

  it('redacts URL credentials in kagent’s warnings', () => {
    const html = render(<KagentAgentRenderer data={agent({ template: { ref: { name: 'base' } } }, [cond('Ready', 'True', 'Ready')], {
      warnings: ['tool server https://user:FAKEPASS@mcp.example/mcp?token=FAKEQUERY exposes every tool'],
    })} />)
    expect(html).not.toContain('FAKEPASS')
    expect(html).not.toContain('FAKEQUERY')
    expect(html).toContain('https://[REDACTED]@mcp.example/mcp?[REDACTED]')
  })
})

it('keeps the page Spec tab focused while the drawer retains its full evidence', () => {
  const data = agent({ template: { inline: { modelConfig: { name: 'llm' }, description: 'Cluster helper' } } }, [cond('Ready', 'True', 'Ready')])
  const spec = render(<KagentAgentRenderer data={data} specOnly sessions={<p>sessions-slot</p>} />)
  expect(spec).toContain('Cluster helper')
  expect(spec).toContain('Conditions')
  for (const text of ['Dependencies', 'Conversation outcomes', 'sessions-slot', 'Ready to start sessions']) expect(spec).not.toContain(text)
  const drawer = render(<KagentAgentRenderer data={data} sessions={<p>sessions-slot</p>} />)
  expect(drawer).toContain('Dependencies')
  expect(drawer).toContain('sessions-slot')
})
