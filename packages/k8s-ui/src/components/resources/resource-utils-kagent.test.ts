import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import {
  getKagentAgentColumnValue,
  getKagentHarnessDeclarations,
  getKagentStatus,
  getKagentTemplateDeclarations,
  kagentReference,
  readKagent,
  redactKagentLiterals,
} from './resource-utils-kagent'

// Shared with pkg/kagent's status_test.go, which also checks the Issues each
// reading raises.
const vectors = JSON.parse(readFileSync(new URL('../../../../../pkg/kagent/testdata/status.json', import.meta.url), 'utf8'))

describe('kagent readings match pkg/kagent', () => {
  it('has vectors', () => expect(vectors.length).toBeGreaterThan(20))
  for (const v of vectors) {
    it(v.name, () => {
      const r = readKagent(v.object, Date.parse(v.now))
      if (!v.want.reads) {
        expect(r).toBeNull()
        return
      }
      expect(r).not.toBeNull()
      expect({ state: r!.state, tone: r!.tone, text: r!.text, summary: r!.summary, egress: r!.egress?.summary ?? '' })
        .toEqual({ state: v.want.state, tone: v.want.tone, text: v.want.text, summary: v.want.summary, egress: v.want.egress ?? '' })
    })
  }
})

describe('getKagentStatus', () => {
  const find = (name: string) => vectors.find((v: any) => v.name === name)
  it('carries the egress summary on a Ready agent whose grants are not applied', () => {
    const v = find('agent ready with an egress policy not found')
    const s = getKagentStatus(v.object, Date.parse(v.now))
    expect(s).toMatchObject({ text: 'Egress not applied', level: 'alert' })
    expect(s?.message).toContain('agents/github')
  })
  it('has no message when healthy', () => {
    const v = find('agent ready')
    expect(getKagentStatus(v.object, Date.parse(v.now))?.message).toBeUndefined()
  })
  it('returns null for spec-only kinds and other groups', () => {
    expect(getKagentStatus(find('harness is spec-only').object)).toBeNull()
    expect(getKagentStatus(find('elastic agent is not kagent\'s').object)).toBeNull()
  })
})

describe('declared references', () => {
  it('defaults kind and namespace, and keeps a foreign group', () => {
    expect(kagentReference({ name: 'gpt' }, 'ModelConfig', 'agents')).toEqual({ kind: 'ModelConfig', group: 'api.kagent.dev', namespace: 'agents', name: 'gpt' })
    expect(kagentReference({ name: 'gh', apiGroup: 'example.com', kind: 'Server', namespace: 'mcp' }, 'RemoteMCPServer', 'agents')).toEqual({ kind: 'Server', group: 'example.com', namespace: 'mcp', name: 'gh' })
    expect(kagentReference({}, 'ModelConfig', 'agents')).toBeUndefined()
  })
  it('reads a template spec', () => {
    const d = getKagentTemplateDeclarations({
      modelConfig: { name: 'gpt', namespace: 'shared' },
      tools: [
        { mcp: { server: { kind: 'RemoteMCPServer', name: 'github' }, tools: ['list_issues'], requireApproval: true } },
        { subAgent: { name: 'reviewer', description: 'Reviews', templateRef: { name: 'reviewer' } } },
      ],
      skills: [{ name: 'triage', source: { oci: 'x' } }],
    }, 'agents')
    expect(d.model).toMatchObject({ namespace: 'shared', name: 'gpt' })
    expect(d.tools[0]).toMatchObject({ server: { name: 'github', namespace: 'agents' }, tools: ['list_issues'], requireApproval: true })
    expect(d.tools[1].subAgent).toEqual({ name: 'reviewer', template: 'reviewer' })
    expect(d.skills).toEqual(['triage'])
  })
  it('reads a Harness', () => {
    const h = getKagentHarnessDeclarations({
      metadata: { namespace: 'agents' },
      spec: { kagent: { memory: { modelConfigRef: { name: 'embed' } }, compaction: { summarizer: { modelConfigRef: { name: 'small' } } } }, substrate: { workerPoolRef: { name: 'pool' } }, env: [{ name: 'MODE', value: 'x' }] },
    })
    expect(h).toMatchObject({ type: 'kagent', workerPool: 'pool', env: ['MODE'] })
    expect(h.memoryModel?.name).toBe('embed')
    expect(h.summarizerModel?.name).toBe('small')
  })
})

describe('redactKagentLiterals', () => {
  it('blanks literal header and environment values, keeping names and Secret references', () => {
    const server = redactKagentLiterals({ apiVersion: 'api.kagent.dev/v1alpha3', kind: 'RemoteMCPServer', spec: { headersFrom: [{ name: 'X-Key', value: 's3cret' }, { name: 'Authorization', valueFrom: { type: 'Secret', name: 'gh', key: 't' } }] } })
    expect(JSON.stringify(server)).not.toContain('s3cret')
    expect(JSON.stringify(server)).toContain('"gh"')
    const model = redactKagentLiterals({ apiVersion: 'api.kagent.dev/v1alpha3', kind: 'ModelConfig', spec: { defaultHeaders: { 'X-Key': 's3cret' }, azureOpenAI: { azureAdToken: 's3cret' } } })
    expect(JSON.stringify(model)).not.toContain('s3cret')
    const harness = redactKagentLiterals({ apiVersion: 'api.kagent.dev/v1alpha3', kind: 'Harness', spec: { env: [{ name: 'MODE', value: 's3cret' }] } })
    expect(JSON.stringify(harness)).not.toContain('s3cret')
  })
  it('leaves other groups alone', () => {
    const foreign = { apiVersion: 'example.com/v1', kind: 'ModelConfig', spec: { defaultHeaders: { k: 'v' } } }
    expect(redactKagentLiterals(foreign)).toBe(foreign)
  })
})

describe('getKagentAgentColumnValue', () => {
  it('reads the template, harness, model and revision', () => {
    const agent = { apiVersion: 'api.kagent.dev/v1alpha3', kind: 'Agent', metadata: { namespace: 'agents' }, spec: { template: { inline: { modelConfig: { name: 'gpt' } } }, harnessRef: { name: 'default' } }, status: { latestSuccessfulRevision: 'a1b2c3d4e5f60000' } }
    expect(getKagentAgentColumnValue(agent, 'kagentTemplate')).toBe('Inline')
    expect(getKagentAgentColumnValue(agent, 'kagentHarness')).toBe('default')
    expect(getKagentAgentColumnValue(agent, 'kagentModel')).toBe('gpt')
    expect(getKagentAgentColumnValue(agent, 'kagentRevision')).toBe('a1b2c3d4e5f6')
    expect(getKagentAgentColumnValue({ ...agent, apiVersion: 'agent.k8s.elastic.co/v1alpha1' }, 'kagentHarness')).toBeUndefined()
  })
})
