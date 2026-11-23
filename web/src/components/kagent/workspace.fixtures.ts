import type { KagentAgent, KagentRef, KagentWorkspace } from '../../api/kagent'
export const ref = (kind: string, name: string, namespace = 'agents'): KagentRef => ({ group: 'api.kagent.dev', kind, namespace, name })
export function workspace(): KagentWorkspace {
  const keys = [
    'agents',
    'agentTemplates',
    'harnesses',
    'models',
    'mcpServers',
    'egressPolicies',
    'modelProviders',
    'sandboxTemplates',
  ] as const
  const coverage = Object.fromEntries(keys.map((k) => [k, { state: 'full' }])) as KagentWorkspace['coverage']
  const count = { n: 0, lowerBound: false }
  return {
    context: 'test',
    installed: true,
    namespaces: [],
    coverage,
    referenceCoverage: coverage,
    agents: [],
    models: [],
    mcpServers: [],
    modelProviders: [],
    findings: [],
    groups: [],
    counts: {
      findings: { ...count },
      critical: { ...count },
      agents: { ...count },
      agentsAffected: { ...count },
      mcpServers: { ...count },
      mcpServersFailing: { ...count },
      models: { ...count },
      modelsWithProblems: { ...count },
      groups: { n: 0, incomplete: false },
      agentsByLatestChange: {},
    },
  }
}
export function agent(name = 'helper'): KagentAgent {
  return {
    ref: ref('Agent', name),
    reading: { state: 'ready', tone: 'healthy', text: 'Ready', summary: 'Ready to start sessions', condition: 'Ready' },
    latestChange: { state: 'prepared' },
    newSessionsRevision: 'abcdef0123456789',
    inlineTemplate: true,
    description: 'Helps operate the cluster',
    mcpServers: { declared: 0, failing: 0, absent: 0, unread: 0, notPermitted: 0 },
    references: [],
  }
}
export function blockedWorkspace(): KagentWorkspace {
  const w = workspace(),
    a = agent('blocked-edit'),
    model = ref('ModelConfig', 'broken-model', 'models')
  a.latestChange = { state: 'blocked', message: 'resolve ModelConfig "models/broken-model": secret missing not found' }
  a.reading = { ...a.reading, tone: 'alert', state: 'blocked', text: 'Blocked', message: a.latestChange.message }
  a.model = {
    ref: model,
    state: 'present',
    reading: { state: 'blocked', tone: 'alert', text: 'Blocked', summary: 'API key Secret missing', reason: 'APIKeySecretNotFound' },
  }
  a.references = [
    { role: 'model', from: a.ref, target: model, state: 'present', field: 'spec.template.inline.modelConfig', reading: a.model.reading },
  ]
  w.agents = [a]
  w.models = [
    {
      ref: model,
      reading: a.model.reading!,
      provider: 'OpenAI',
      model: 'gpt',
      credential: { mode: 'apiKeySecret', secret: { ...ref('Secret', 'missing', 'models'), group: '' }, key: 'OPENAI_API_KEY' },
      usedBy: { count: 1, lowerBound: false, consumers: 'seen', agents: [{ agent: a.ref, effect: 'blocked', roles: ['model'] }] },
    },
  ]
  w.findings = [
    {
      id: 'agent',
      subject: a.ref,
      severity: 'warning',
      reason: 'KagentChangeBlocked',
      message: 'Blocked edit',
      action: 'Fix the model.',
      cause: { target: model, form: 'failed', role: 'model' },
    },
    {
      id: 'model',
      subject: model,
      severity: 'warning',
      reason: 'KagentAPIKeySecretMissing',
      kagentReason: 'APIKeySecretNotFound',
      message: 'secret missing not found',
      action: 'Create Secret models/missing.',
      affects: w.models[0].usedBy,
    },
  ]
  w.groups = [{ id: 'model', severity: 'warning', findings: ['model'], folded: [{ finding: 'agent', under: 'model' }] }]
  w.counts.findings.n = 2
  w.counts.groups.n = 1
  w.counts.agents.n = 1
  w.counts.models.n = 1
  w.counts.modelsWithProblems.n = 1
  w.counts.agentsByLatestChange = { blocked: 1 }
  return w
}
