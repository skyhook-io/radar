import { AnalysisStory, EvidenceCard, RuledOutBlock } from '@skyhook-io/k8s-ui'

// Investigation of Deployment payments/checkout-api crash-looping after the
// v2.15.0 rollout. Shapes match what Radar's evidence projection produces
// from the agent's diagnose / get_pod_logs / get_resource calls.
const POD = 'checkout-api-7d9f8c6b5-x2kqp'
const TARGET = 'deployment payments/checkout-api'

function source(stepId: string, tool: string, args: object, order: number, primaryGroupId: string | undefined, evidenceRef: string) {
  return {
    id: `turn-0-step-${stepId}`, turnIndex: 0, timelineIndex: order, stepId, tool, args: JSON.stringify(args),
    order, phase: 'initial' as const, confirmedSuccess: true, evidenceRef, primaryGroupId,
  }
}
function group(id: string, kind: string, source: ReturnType<typeof source>, o: Record<string, unknown>) {
  const observation = { source, revision: 1, historical: false, changedFromPrevious: false, ...o } as any
  return { id, identity: id, kind, historical: false, firstOrder: source.order, observations: [observation], latest: observation, chronologicalLatest: observation } as any
}

const diagnoseCall = source('diag-1', 'diagnose', { kind: 'deployment', namespace: 'payments', name: 'checkout-api' }, 0, 'evidence-issue-crashloop', 'ev_diag1')
const logsCall = source('logs-1', 'get_pod_logs', { namespace: 'payments', name: POD, container: 'checkout-api', previous: true, tail_lines: 100 }, 1, 'evidence-logs-previous', 'ev_logs1')
const nodeCall = source('node-1', 'get_resource', { kind: 'node', name: 'ip-10-0-3-41.ec2.internal' }, 2, 'evidence-node', 'ev_node1')

const issueGroup = group('evidence-issue-crashloop', 'issue', diagnoseCall, {
  tier: 'key', tone: 'error', relevance: 'target', title: 'CrashLoopBackOff',
  summary: '3/3 pods of checkout-api are in CrashLoopBackOff (container checkout-api restarted 14 times).',
  data: { type: 'issue', relevance: 'target', issue: {
    id: 'issue-checkout-crash', severity: 'critical', source: 'problem', category: 'crashloop', category_group: 'runtime', grouping_scope: 'workload',
    kind: 'Deployment', group: 'apps', namespace: 'payments', name: 'checkout-api', reason: 'CrashLoopBackOff',
    message: '3/3 pods of checkout-api are in CrashLoopBackOff (container checkout-api restarted 14 times).',
  } },
})
const deploymentGroup = group('evidence-deployment', 'resource', diagnoseCall, {
  tier: 'supporting', relevance: 'target', tone: 'error', title: 'Deployment payments/checkout-api', summary: '0/3 replicas ready',
  data: { type: 'resource', warnings: [],
    resource: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { namespace: 'payments', name: 'checkout-api' }, spec: { replicas: 3 }, status: { replicas: 3, readyReplicas: 0, unavailableReplicas: 3 } },
    resourceContext: { tier: 'diagnostic', issueSummary: { count: 1, highestSeverity: 'critical', topReason: 'CrashLoopBackOff' }, workloadSummary: { replicas: { desired: 3, ready: 0, available: 0, unavailable: 3 } } },
  },
})
const crashGroup = group('evidence-crash-oomkilled', 'crash', diagnoseCall, {
  tier: 'key', relevance: 'producer-related', tone: 'error', title: 'checkout-api OOMKilled',
  summary: 'INFO  c.a.checkout.cache.ProductCache - warming product cache (48,210 SKUs)',
  data: { type: 'crash', namespace: 'payments', crash: {
    pods: [POD, 'checkout-api-7d9f8c6b5-m4v7t', 'checkout-api-7d9f8c6b5-zq8wn'], container: 'checkout-api', state: 'terminated', reason: 'OOMKilled', exitCode: 137,
    logLine: 'INFO  c.a.checkout.cache.ProductCache - warming product cache (48,210 SKUs)', logSource: 'previous', logLineSelection: 'log_tail',
  } },
})
const eventsGroup = group('evidence-events', 'events', diagnoseCall, {
  tier: 'supporting', relevance: 'producer-related', tone: 'warning', title: 'Kubernetes events',
  summary: `BackOff: Back-off restarting failed container checkout-api in pod ${POD} · 2 event groups · ${TARGET}`,
  data: { type: 'events', scope: TARGET, events: [
    { reason: 'BackOff', message: `Back-off restarting failed container checkout-api in pod ${POD}`, type: 'Warning', count: 14, lastTimestamp: '2026-09-28T09:41:12Z' },
    { reason: 'Pulled', message: 'Container image "ghcr.io/acme/checkout-api:v2.15.0" already present on machine', type: 'Normal', count: 15, lastTimestamp: '2026-09-28T09:40:31Z' },
  ] },
})
const changesGroup = group('evidence-changes', 'changes', diagnoseCall, {
  tier: 'context', relevance: 'producer-related', tone: 'info', title: 'Recent changes', summary: `1 change · ${TARGET}`,
  data: { type: 'changes', scope: TARGET, subject: { kind: 'Deployment', namespace: 'payments', name: 'checkout-api' }, changes: [
    { kind: 'Deployment', namespace: 'payments', name: 'checkout-api', changeType: 'update', timestamp: '2026-09-28T09:12:04Z', summary: 'image ghcr.io/acme/checkout-api:v2.14.1 → v2.15.0' },
  ] },
})
const logsGroup = group('evidence-logs-previous', 'logs', logsCall, {
  tier: 'context', relevance: 'broader', tone: 'neutral', title: `Previous logs · ${POD} / checkout-api`, summary: '3 selected lines',
  data: { type: 'logs', pod: POD, container: 'checkout-api', namespace: 'payments', previous: true, warnings: [], logs: {
    lines: [
      '2026-09-28T09:40:52Z INFO  o.s.b.StartupInfoLogger - Starting CheckoutApplication v2.15.0 using Java 21.0.4',
      '2026-09-28T09:41:05Z INFO  c.a.checkout.cache.ProductCache - warming product cache (48,210 SKUs)',
      '2026-09-28T09:41:09Z WARN  c.a.checkout.cache.ProductCache - heap usage 238Mi / 256Mi during warm-up',
    ], totalLines: 100, matchedLines: 3, fallback: false,
  } },
})
const nodeGroup = group('evidence-node', 'resource', nodeCall, {
  tier: 'context', relevance: 'broader', tone: 'neutral', title: 'Node ip-10-0-3-41.ec2.internal',
  data: { type: 'resource', warnings: [], resource: { apiVersion: 'v1', kind: 'Node', metadata: { name: 'ip-10-0-3-41.ec2.internal' }, status: {
    conditions: [
      { type: 'MemoryPressure', status: 'False', reason: 'KubeletHasSufficientMemory', message: 'kubelet has sufficient memory available' },
      { type: 'Ready', status: 'True', reason: 'KubeletReady', message: 'kubelet is posting ready status' },
    ],
    allocatable: { cpu: '3920m', memory: '14.2Gi' },
  } } },
})

// The agent's case: each cited result with its role, bound to one card.
const evidence = [
  { status: 'linked' as const, ref: 'ev_diag1', role: 'cause' as const, claim: 'Every pod is OOMKilled (exit 137) ~40s after start — the 256Mi limit is hit during cache warm-up.', subject: { kind: 'Deployment', namespace: 'payments', name: 'checkout-api', observation: 'crash' } },
  { status: 'linked' as const, ref: 'ev_logs1', role: 'symptom' as const, claim: 'The log ends mid warm-up with heap at 238Mi of 256Mi — no application error.', gap: 'Only the last 100 lines of the previous container were read.' },
  { status: 'linked' as const, ref: 'ev_diag1', role: 'context' as const, claim: 'The crash loop began after the v2.15.0 rollout, which added the in-memory product cache.', subject: { kind: 'Deployment', namespace: 'payments', name: 'checkout-api', observation: 'changes' } },
  { status: 'linked' as const, ref: 'ev_node1', role: 'rules_out' as const, claim: 'The node reports MemoryPressure=False with 14.2Gi allocatable, so this is not node-level eviction.' },
]
const cardItem = (index: number, g: any) => {
  const { status: _s, ref: _r, ...rest } = evidence[index]
  return { index, ...rest, source: g.latest.source, placement: 'card' as const, groupId: g.id, observation: g.latest } as any
}
const caseItems = [cardItem(0, crashGroup), cardItem(1, logsGroup), cardItem(2, changesGroup), cardItem(3, nodeGroup)]
const investigationCase = {
  items: caseItems,
  ruledOut: [{ hypothesis: 'Node memory pressure is evicting the pods', item: caseItems[3] }],
}
const report = [
  'Every checkout-api pod is killed by the kernel about 40 seconds after it starts. The container’s last state is **OOMKilled** with exit code 137:',
  '',
  '[[radar:evidence=0]]',
  '',
  'The previous container’s log ends in the middle of cache warm-up, with the JVM reporting 238Mi of heap against a 256Mi limit — no exception, just a process that stopped:',
  '',
  '[[radar:evidence=1]]',
  '',
  'This started with the v2.15.0 rollout at 09:12, which introduced the in-memory product cache [[radar:evidence=2]]. The node itself has plenty of memory [[radar:evidence=3]], so the limit is the container’s, not the node’s.',
].join('\n')

const groupsById = new Map([crashGroup, logsGroup, changesGroup, nodeGroup].map((g) => [g.id, g]))
const target = (index: number) => ({ item: caseItems[index], domId: `story-${caseItems[index].groupId}` })

// What resolveStoryPlacements produces for `report`: block markers place
// their card; the two inline markers in the last paragraph get placed under it.
const placements = {
  segments: [
    { kind: 'prose', markdown: 'Every checkout-api pod is killed by the kernel about 40 seconds after it starts. The container’s last state is **OOMKilled** with exit code 137:' },
    { kind: 'placement', index: 0 },
    { kind: 'prose', markdown: 'The previous container’s log ends in the middle of cache warm-up, with the JVM reporting 238Mi of heap against a 256Mi limit — no exception, just a process that stopped:' },
    { kind: 'placement', index: 1 },
    { kind: 'prose', markdown: 'This started with the v2.15.0 rollout at 09:12, which introduced the in-memory product cache. The node itself has plenty of memory, so the limit is the container’s, not the node’s.' },
    { kind: 'placement', index: 2, auto: true },
    { kind: 'placement', index: 3, auto: true },
  ],
  byIndex: new Map([0, 1, 2, 3].map((i) => [i, { kind: 'placed', target: target(i) }])),
  placedAt: new Map([[0, 1], [1, 3], [2, 5], [3, 6]]),
  placedCount: 4,
  lostItems: 0,
} as any

const withLoss = {
  segments: [
    { kind: 'prose', markdown: 'The pods restart because the container is **OOMKilled** at its 256Mi limit:' },
    { kind: 'placement', index: 0 },
    { kind: 'prose', markdown: 'Memory climbs steadily during cache warm-up, peaking just under the limit before the kill:' },
    { kind: 'placement', index: 4 },
    { kind: 'prose', markdown: 'The HPA did not scale out during the incident either:' },
    { kind: 'placement', index: 5 },
  ],
  byIndex: new Map<number, any>([
    [0, { kind: 'placed', target: target(0) }],
    [4, { kind: 'lost', reason: { kind: 'nocard', sourceId: 'turn-0-step-prom-1' } }],
    [5, { kind: 'lost', reason: 'unlinked' }],
  ]),
  placedAt: new Map([[0, 1]]),
  placedCount: 1,
  lostItems: 2,
} as any

const noop = () => {}
const card = ({ item }: any, { compact }: { compact: boolean }) => (
  <EvidenceCard group={groupsById.get(item.groupId)} observation={item.observation} domId={`story-${item.groupId}`}
    noteMode="chip" placedInStory compact={compact} animateArrival={false} onViewSource={noop} spanFullRow />
)
const frame = { width: 680, padding: 16, borderRadius: 12, border: '1px solid var(--border)' }

export function Folded() {
  return (
    <div style={frame}>
      <AnalysisStory placements={placements} renderPlacement={card} onReveal={noop} onViewSource={noop}
        trailing={<RuledOutBlock entries={investigationCase.ruledOut} onReveal={noop} />} />
    </div>
  )
}

// The full analysis behind the fold, ending in the agent's ruled-out block.
const expanded = {
  ...placements,
  segments: placements.segments.filter((_: unknown, i: number) => i !== 2 && i !== 3),
  byIndex: new Map([0, 2, 3].map((i) => [i, { kind: 'placed', target: target(i) }])),
  placedAt: new Map([[0, 1], [2, 3], [3, 4]]),
  placedCount: 3,
}

export function Expanded() {
  return (
    <div style={frame}>
      <AnalysisStory placements={expanded} renderPlacement={card} onReveal={noop} onViewSource={noop} openRequest={1}
        trailing={<RuledOutBlock entries={investigationCase.ruledOut} onReveal={noop} />} />
    </div>
  )
}

export function LostSupport() {
  return (
    <div style={frame}>
      <AnalysisStory placements={withLoss} renderPlacement={card} onReveal={noop} onViewSource={noop} openRequest={1} />
    </div>
  )
}
