import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
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
})
