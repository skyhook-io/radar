import type {
  SubstrateAPITier,
  SubstrateConnection,
  SubstrateActorDetail,
  SubstrateActorRow,
  SubstrateActors,
  SubstrateAttention,
  SubstrateStateCounts,
  SubstrateStatus,
  SubstrateWorkspace,
} from '../../api/substrateWorkspace'

// Shaped exactly as /api/substrate/* serve them (pkg/substrate,
// internal/server/substrate_workspace_handlers.go): absent fields are
// values Substrate or Radar did not read.

export const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()

export const connectedTier: SubstrateAPITier = {
  state: 'connected',
  identity: { mode: 'shared', principal: 'system:serviceaccount:ate-system:ate-client' },
  readAt: minutesAgo(0.1),
}

export const notConfiguredTier: SubstrateAPITier = {
  state: 'notConfigured',
  code: 'notConfigured',
  reason: 'Choose who Radar reads Substrate’s API as.',
}

export const sharedTier: SubstrateAPITier = {
  state: 'disabled',
  code: 'sharedRadar',
  reason: "This Radar is shared, so it does not read Substrate's API: Substrate authorizes by its own roles, and every user would see what Radar's one identity sees. Use a local Radar (CLI or Desktop) to see actors, templates and workers.",
}

/** Radar Hub, a user who isn't an org owner: Substrate's API tier is off for them. */
export const ownersOnlyTier: SubstrateAPITier = {
  state: 'disabled',
  code: 'ownersOnly',
  reason: 'In Radar Hub only org owners read Substrate’s API.',
}

/** Radar Hub, an org owner: the shared client identity, minted with the owner's Kubernetes access. */
export const ownerTier: SubstrateAPITier = {
  ...connectedTier,
  endpoint: 'api.ate-system.svc:443',
  identity: { mode: 'sharedAsCaller', principal: 'system:serviceaccount:ate-system:ate-client' },
}

export function counts(byState: Partial<SubstrateStateCounts['byState']>): SubstrateStateCounts {
  const full = { running: 0, suspended: 0, resuming: 0, suspending: 0, reverting: 0, crashed: 0, deleting: 0, unknown: 0, ...byState }
  return { total: Object.values(full).reduce((a, b) => a + b, 0), byState: full }
}

export function status(overrides: Partial<SubstrateStatus> = {}): SubstrateStatus {
  return {
    installed: true,
    apiService: { namespace: 'ate-system', name: 'api', port: 443, found: true },
    apiTier: connectedTier,
    coverage: { pools: { state: 'full' } },
    canConfigure: true,
    ...overrides,
  }
}

export const crashedRow: SubstrateAttention = {
  id: 'crashedActors',
  rule: 'crashedActors',
  severity: 'critical',
  title: '14 actors are crashed',
  reading: "Latest: research/lit-review-0193. Substrate's message: container agent: OOMKilled (limit 2Gi)",
  nextStep: "Fix the cause Substrate's crash message names, then revert each actor to its newest stored generation.",
  command: 'kubectl ate revert actor <name> -a <atespace>',
  subject: { kind: 'Actors', filter: { state: 'crashed' } },
  count: 14,
  since: minutesAgo(4),
  groups: [
    { subject: { kind: 'ActorTemplate', atespace: 'research', name: 'langgraph-research' }, count: 11 },
    { subject: { kind: 'ActorTemplate', atespace: 'code-review', name: 'claude-code-gvisor' }, count: 3 },
  ],
  messages: [
    { text: 'container agent: OOMKilled (limit 2Gi)', count: 11 },
    { text: 'exit 1: git clone: authentication failed', count: 3 },
  ],
  sample: [{ atespace: 'research', name: 'lit-review-0193' }],
}

export const poolShortRow: SubstrateAttention = {
  id: 'poolShort:agent-ns/agent-pool',
  rule: 'poolShort',
  severity: 'warning',
  title: 'agent-ns / agent-pool is short of workers',
  reading: '5 of 6 desired workers are active. New and resuming actors may wait or fail to start.',
  nextStep: "Open the pool: compare its worker Pods with Substrate's worker states to see which side is missing.",
  subject: { kind: 'WorkerPool', namespace: 'agent-ns', name: 'agent-pool' },
  count: 1,
}

export const goldenRow: SubstrateAttention = {
  id: 'goldenSnapshotFailed:sandbox-dev/mcp-browser',
  rule: 'goldenSnapshotFailed',
  severity: 'warning',
  title: 'sandbox-dev/mcp-browser has no golden snapshot, so new actors not seeded from a tag start from a cold boot',
  reading: "Substrate's message: warm-up: container browser exited 137. Substrate does not retry a failed build.",
  nextStep: 'Templates are immutable, so fix the cause and create the template again: delete and recreate it, or create it under a new name. Substrate builds a golden snapshot for each new template.',
  subject: { kind: 'ActorTemplate', atespace: 'sandbox-dev', name: 'mcp-browser' },
  count: 1,
  since: minutesAgo(25),
}

const pool = {
  namespace: 'agent-ns',
  name: 'agent-pool',
  kubernetes: { namespace: 'agent-ns', name: 'agent-pool', desired: 6, ready: 6, sandboxClasses: ['gvisor'], health: 'degraded', healthReason: '5 of 6 workers active' },
  sandboxClasses: ['gvisor'],
}

/** A connected workspace: three pools, 1,284 actors, five rows needing attention. */
export function workspace(overrides: Partial<SubstrateWorkspace> = {}): SubstrateWorkspace {
  return {
    apiTier: connectedTier,
    coverage: {
      actors: { state: 'full', count: 1284 },
      templates: { state: 'full', count: 6 },
      workers: { state: 'full', count: 20 },
      atespaces: { state: 'full', count: 4 },
      pools: { state: 'full' },
    },
    summary: {
      pools: { total: 3, short: 1, full: 1 },
      workers: { total: 20, active: 18, draining: 1, unavailable: 1 },
      slots: { used: 212, total: 236 },
      freeSlots: [
        { pool: { namespace: 'batch', name: 'batch-pool' }, free: 21 },
        { pool: { namespace: 'agent-ns', name: 'agent-pool' }, free: 3 },
      ],
      actors: { total: 1284, crashed: 14 },
      templates: { total: 6, goldenFailed: 1, noMatchingPool: 0 },
    },
    actorStates: counts({ running: 212, suspended: 1041, resuming: 6, suspending: 9, reverting: 2, crashed: 14 }),
    needsAttention: [crashedRow, poolShortRow, goldenRow],
    checks: [
      { rule: 'crashedActors', label: 'Crashed actors', ran: true },
      { rule: 'poolShort', label: 'Pools short of workers', ran: true },
      { rule: 'goldenSnapshotFailed', label: 'Failed golden snapshots', ran: true },
    ],
    notAssessed: ['API operation errors (resume, suspend and tag calls)', 'Request routing and egress'],
    service: { read: false, why: 'Prometheus is not connected', poolsUnknown: 3, nodesOverdue: 0, nodesUnknown: 0 },
    pools: [
      {
        ...pool,
        placement: { workers: { total: 6, active: 5, draining: 0, unavailable: 1 }, slots: { used: 37, total: 40 }, cpuMillicores: { used: 9000, total: 12000 }, memoryBytes: { used: 1, total: 2 }, complete: true },
        actors: 37,
        capacity: 'short',
      },
      {
        namespace: 'agent-ns',
        name: 'microvm-pool',
        kubernetes: { namespace: 'agent-ns', name: 'microvm-pool', desired: 4, ready: 4, health: 'healthy', healthReason: '' },
        placement: { workers: { total: 4, active: 4, draining: 0, unavailable: 0 }, slots: { used: 16, total: 16 }, cpuMillicores: { used: 1, total: 1 }, memoryBytes: { used: 1, total: 1 }, complete: true },
        actors: 16,
        sandboxClasses: ['microvm'],
        capacity: 'full',
      },
      {
        namespace: 'batch',
        name: 'batch-pool',
        kubernetes: { namespace: 'batch', name: 'batch-pool', desired: 10, ready: 9, health: 'healthy', healthReason: '' },
        placement: { workers: { total: 10, active: 9, draining: 1, unavailable: 0 }, slots: { used: 159, total: 180 }, cpuMillicores: { used: 1, total: 2 }, memoryBytes: { used: 1, total: 2 }, complete: true },
        actors: 159,
        sandboxClasses: ['gvisor'],
        capacity: 'free',
      },
    ],
    atespaces: [
      { name: 'support-bots', actorsRead: 'full', actors: counts({ running: 60, suspended: 550, crashed: 1 }), templatesRead: 'full', templates: 1 },
      { name: 'research', actorsRead: 'full', actors: counts({ running: 40, suspended: 121, crashed: 11 }), templatesRead: 'full', templates: 2 },
      { name: 'restricted', actorsRead: 'denied', templatesRead: 'denied' },
    ],
    ...overrides,
  }
}

/** The same cluster with Substrate's API not connected: Kubernetes' pools only. */
export function notConnectedWorkspace(tier: SubstrateAPITier = notConfiguredTier): SubstrateWorkspace {
  return {
    apiTier: tier,
    coverage: { pools: { state: 'full' } },
    summary: { pools: { total: 1, short: 0, full: 0 } },
    needsAttention: [],
    checks: [
      { rule: 'poolShort', label: 'Pools short of workers', ran: true },
      { rule: 'crashedActors', label: 'Crashed actors', ran: false, why: 'actors were not read' },
    ],
    notAssessed: ['Request routing and egress'],
    service: { read: false, why: 'Prometheus is not connected', poolsUnknown: 1, nodesOverdue: 0, nodesUnknown: 0 },
    pools: [{ ...pool, capacity: undefined }],
  }
}

export function actorRow(overrides: Partial<SubstrateActorRow> = {}): SubstrateActorRow {
  return {
    atespace: 'research',
    name: 'lit-review-0193',
    state: 'crashed',
    reason: 'container agent: OOMKilled (limit 2Gi)',
    template: { atespace: 'research', name: 'langgraph-research' },
    node: 'ip-10-0-4-57',
    newestSnapshot: { generation: 14, fidelity: 'memory', owner: 'actor', durable: 'completed', sandboxClass: 'gvisor' },
    resumeGeneration: 14,
    resumesWith: 'memory',
    createTime: minutesAgo(2 * 24 * 60),
    updateTime: minutesAgo(4),
    ...overrides,
  }
}

export function actors(overrides: Partial<SubstrateActors> = {}): SubstrateActors {
  const rows = overrides.rows ?? [
    actorRow(),
    actorRow({ name: 'ticket-41207', atespace: 'support-bots', state: 'suspended', reason: 'Upload pending for 12m', newestSnapshot: { generation: 7, fidelity: 'memory', durable: 'pending', locality: 'ip-10-0-4-57' }, resumeGeneration: 7 }),
    actorRow({ name: 'pr-8790-reviewer', atespace: 'code-review', state: 'running', reason: undefined, newestSnapshot: undefined, resumeGeneration: undefined, resumesWith: 'cold', worker: { worker: 'w-1', namespace: 'batch', pool: 'batch-pool', pod: 'batch-pool-5d2b-r8t' }, node: 'ip-10-0-6-11' }),
  ]
  return {
    apiTier: connectedTier,
    coverage: { state: 'full', count: 1284 },
    sources: ['snapshot'],
    total: 1284,
    counts: counts({ running: 212, suspended: 1041, resuming: 6, suspending: 9, reverting: 2, crashed: 14 }),
    rows,
    limit: 50,
    offset: 0,
    ...overrides,
  }
}

export function actorDetail(overrides: Partial<SubstrateActorDetail> = {}): SubstrateActorDetail {
  const row = actorRow()
  return {
    apiTier: connectedTier,
    actor: {
      atespace: 'research',
      name: 'lit-review-0193',
      uid: 'a-1',
      template: { atespace: 'research', name: 'langgraph-research' },
      state: 'crashed',
      assignedNode: 'ip-10-0-4-57',
      crash: { message: 'container agent: OOMKilled (limit 2Gi)', time: minutesAgo(4) },
      snapshots: [
        { generation: 14, fidelity: 'memory', owner: 'actor', durable: 'completed', sandboxClass: 'gvisor' },
        { generation: 13, fidelity: 'memory', owner: 'actor', durable: 'completed', sandboxClass: 'gvisor' },
      ],
      createTime: minutesAgo(2 * 24 * 60),
      updateTime: minutesAgo(4),
    },
    row,
    template: {
      atespace: 'research',
      name: 'langgraph-research',
      golden: { state: 'ready', tag: { atespace: 'research', name: 'golden' }, time: minutesAgo(600) },
      actors: counts({ suspended: 150, running: 10, crashed: 11 }),
      matchingPools: [{ namespace: 'agent-ns', name: 'agent-pool' }],
      matchingPoolsComplete: true,
      cpuMillicores: 2000,
      memoryBytes: 2 * 1024 ** 3,
      preferredFidelity: 'memory',
      containers: ['agent', 'sidecar'],
    },
    egress: { state: 'set', rules: 2, destinations: ['api.openai.com', '*.arxiv.org'] },
    related: [
      { kind: 'ActorTemplate', atespace: 'research', name: 'langgraph-research' },
      { kind: 'Node', name: 'ip-10-0-4-57' },
    ],
    // On an actor's page the row's command names the actor.
    attention: [{ ...crashedRow, command: 'kubectl ate revert actor lit-review-0193 -a research' }],
    coverage: { actors: { state: 'full' }, templates: { state: 'full' }, workers: { state: 'full' }, pools: { state: 'full' } },
    matchingPools: [{ namespace: 'agent-ns', name: 'agent-pool' }],
    matchingPoolsComplete: true,
    recovery: { generation: 14, fidelity: 'memory', resumesWith: 'memory', discarded: [] },
    ...overrides,
  }
}

/** GET /connection for a context with nothing saved. */
export function connection(overrides: Partial<SubstrateConnection> = {}): SubstrateConnection {
  return {
    target: { binding: 'b', context: 'kind-demo', fingerprint: 'f', clientGeneration: 1, operationGeneration: 1 },
    saved: false,
    sharedPrincipal: 'system:serviceaccount:ate-system:ate-client',
    ...overrides,
  }
}
