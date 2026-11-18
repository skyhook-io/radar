// GET /api/agentsandbox/workspace?namespaces=radar-agents-live as a Radar
// built from this branch answered it against the live test cluster (a real
// agent-sandbox controller v1.0.11): nine Sandboxes, two claims, one template,
// one warm pool short of ready. Times are relative to now.
import type { AgentSandboxAttention, AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'

const NS = 'radar-agents-live'
const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()
export const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()

const sandbox = (name: string) => ({ kind: 'Sandbox', group: 'agents.x-k8s.io', namespace: NS, name })
const claim = (name: string) => ({ kind: 'SandboxClaim', group: 'extensions.agents.x-k8s.io', namespace: NS, name })
const pool = (name: string) => ({ kind: 'SandboxWarmPool', group: 'extensions.agents.x-k8s.io', namespace: NS, name })
const pod = (name: string) => ({ kind: 'Pod', group: '', namespace: NS, name })

export function liveAttention(): AgentSandboxAttention[] {
  // In the server's order: severity, then rule, then newest first.
  return [
    {
      id: `sandbox:${NS}/failed`, category: 'sandbox', rule: 'failed', reason: 'PodFailed', severity: 'critical',
      title: 'Pod failed', nextStep: 'Read the Pod’s logs to see why it exited',
      subject: sandbox('failed'), related: { lead: 'Its Pod:', refs: [pod('failed')] }, issue: true,
    },
    {
      id: `sandbox:${NS}/bad-image`, category: 'sandbox', rule: 'failed', reason: 'ErrImagePull', severity: 'critical',
      title: 'Image can’t be pulled',
      rawDetail: 'Pod exists with phase: Pending; container "agent" is waiting: ErrImagePull', rawDetailLabel: 'Controller’s message', rawDetailSource: 'controller',
      nextStep: 'Check the image name and the registry credentials (imagePullSecrets) in the Sandbox’s Pod template',
      subject: sandbox('bad-image'), related: { lead: 'Its Pod:', refs: [pod('bad-image')] }, issue: true,
    },
    {
      id: `sandbox:${NS}/foreign`, category: 'sandbox', rule: 'suspensionBlocked', reason: 'PodNotOwned', severity: 'alert',
      title: 'Suspension blocked',
      reading: 'The controller refuses to delete a Pod this Sandbox doesn’t own, so the suspension waits.',
      rawDetail: 'Refused to delete pod because it is not owned by this sandbox', rawDetailLabel: 'Controller’s message', rawDetailSource: 'controller',
      nextStep: 'Remove Pod foreign, which this Sandbox doesn’t own, or recreate the Sandbox under another name',
      subject: sandbox('foreign'), related: { lead: 'The Pod it won’t delete:', refs: [pod('foreign')] }, issue: true,
    },
    {
      id: `warmPool:${NS}/warm`, category: 'warmPool', rule: 'reserveBelowTarget', reason: 'Warming', severity: 'alert',
      title: 'Warm reserve below target: 0/2 ready',
      reading: 'Its 2 sandboxes can’t be scheduled. No sandbox is ready in reserve, so a new claim can’t take a warm one and waits for a sandbox to start.',
      rawDetail: '0/4 nodes are available: 4 Too many pods. no new claims to deallocate, preemption: 0/4 nodes are available: 4 No preemption victims found for incoming pod.',
      rawDetailLabel: 'Scheduler’s message', rawDetailSource: 'scheduler',
      nextStep: 'Free capacity on a node, or change the Pod template’s requests, node selector or tolerations',
      subject: pool('warm'), related: { lead: 'Not ready:', refs: [sandbox('warm-nd2zw'), sandbox('warm-4xmkm')] },
      relations: [{ role: 'template', ref: { kind: 'SandboxTemplate', group: 'extensions.agents.x-k8s.io', namespace: NS, name: 'tmpl' }, presence: 'present' }],
      issue: false,
    },
    {
      id: `claim:${NS}/claim-missing-pool`, category: 'claim', rule: 'cannotBind', reason: 'WarmPoolNotFound', severity: 'warning',
      title: 'Warm pool not found',
      rawDetail: 'SandboxWarmPool "no-such-pool" not found', rawDetailLabel: 'Controller’s message', rawDetailSource: 'controller',
      nextStep: 'Create SandboxWarmPool no-such-pool in radar-agents-live, or point the claim at an existing pool',
      subject: claim('claim-missing-pool'),
      relations: [{ role: 'warmPool', ref: pool('no-such-pool'), presence: 'absent' }],
      issue: true,
    },
  ]
}

export function liveWorkspace(overrides: Partial<AgentSandboxWorkspaceResponse> = {}): AgentSandboxWorkspaceResponse {
  return {
    context: 'radar-test-management',
    installed: true,
    namespaces: [NS],
    coverage: { claims: { state: 'full' }, sandboxes: { state: 'full' }, templates: { state: 'full' }, warmPools: { state: 'full' } },
    controller: { state: 'running', namespace: 'agent-sandbox-system', name: 'agent-sandbox-controller', ready: 1, desired: 1 },
    counts: {
      sandboxes: { total: 9, lowerBound: false, running: 0, starting: 0, unschedulable: 2, suspended: 1, suspensionBlocked: 1, expired: 2, finished: 1, failed: 2, terminating: 0, unknown: 0 },
      claims: { total: 2, lowerBound: false, bound: 0, waiting: 1, expired: 1 },
      warmPools: { total: 1, lowerBound: false, ready: 0, desired: 2 },
    },
    attention: liveAttention(),
    attentionTotal: 5,
    attentionComplete: true,
    sidebar: {
      total: { count: 5, lowerBound: false },
      sandboxes: { count: 3, lowerBound: false },
      claims: { count: 1, lowerBound: false },
      warmPools: { count: 1, lowerBound: false },
      controller: false,
    },
    warmPools: [
      { namespace: NS, name: 'warm', state: { text: 'Warming', level: 'degraded' }, ready: 0, desired: 2, template: 'tmpl', templatePresence: 'present', claims: { total: 1, bound: 0 }, created: hoursAgo(42) },
    ],
    warmPoolsTotal: 1,
    expired: {
      recent: [
        { ref: sandbox('running'), at: hoursAgo(39) },
        { ref: claim('claim-ok'), at: hoursAgo(39) },
        { ref: sandbox('expired'), at: hoursAgo(43) },
      ],
      recentTotal: 3,
      lowerBound: false,
    },
    ...overrides,
  }
}

/** Nothing installed in scope beyond the CRDs: every kind read in full and empty. */
export function emptyWorkspace(overrides: Partial<AgentSandboxWorkspaceResponse> = {}): AgentSandboxWorkspaceResponse {
  return liveWorkspace({
    counts: {
      sandboxes: { total: 0, lowerBound: false, running: 0, starting: 0, unschedulable: 0, suspended: 0, suspensionBlocked: 0, expired: 0, finished: 0, failed: 0, terminating: 0, unknown: 0 },
      claims: { total: 0, lowerBound: false, bound: 0, waiting: 0, expired: 0 },
      warmPools: { total: 0, lowerBound: false, ready: 0, desired: 0 },
    },
    attention: [],
    attentionTotal: 0,
    sidebar: { total: { count: 0, lowerBound: false }, sandboxes: { count: 0, lowerBound: false }, claims: { count: 0, lowerBound: false }, warmPools: { count: 0, lowerBound: false }, controller: false },
    warmPools: [],
    warmPoolsTotal: 0,
    expired: { recent: [], recentTotal: 0, lowerBound: false },
    ...overrides,
  })
}
