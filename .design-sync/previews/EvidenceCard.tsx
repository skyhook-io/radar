import { EvidenceCard } from '@skyhook-io/k8s-ui'

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

const pullCall = source('diag-2', 'diagnose', { kind: 'deployment', namespace: 'payments', name: 'checkout-api' }, 0, 'evidence-startup-imagepull', 'ev_diag2')
const startupGroup = group('evidence-startup-imagepull', 'startup', pullCall, {
  tier: 'key', relevance: 'producer-related', tone: 'error', title: 'ImagePullBackOff',
  summary: 'Back-off pulling image "ghcr.io/acme/checkout-api:v2.15.0": manifest unknown',
  data: { type: 'startup', pods: ['checkout-api-5c8b9d7f4-hq2lm'], subject: { kind: 'Pod', namespace: 'payments', name: 'checkout-api-5c8b9d7f4-hq2lm' },
    blocker: { kind: 'Pod', name: 'checkout-api-5c8b9d7f4-hq2lm', reason: 'ImagePullBackOff', severity: 'critical', message: 'Back-off pulling image "ghcr.io/acme/checkout-api:v2.15.0": manifest unknown' } },
})

const wrap = { width: 520, padding: 8 }
const noop = () => {}

export function CrashCause() {
  return <div style={wrap}><EvidenceCard group={crashGroup} prominence="primary" animateArrival={false} onViewSource={noop} spanFullRow /></div>
}

export function StartupBlocker() {
  return <div style={wrap}><EvidenceCard group={startupGroup} prominence="primary" animateArrival={false} onViewSource={noop} spanFullRow /></div>
}

export function PreviousLogsInStory() {
  return <div style={wrap}><EvidenceCard group={logsGroup} noteMode="chip" placedInStory domId="story-evidence-logs-previous" prominence="supporting" animateArrival={false} onViewSource={noop} spanFullRow /></div>
}

export function Events() {
  return <div style={wrap}><EvidenceCard group={eventsGroup} prominence="supporting" animateArrival={false} onViewSource={noop} spanFullRow /></div>
}

export function RecentChanges() {
  return <div style={wrap}><EvidenceCard group={changesGroup} prominence="secondary" animateArrival={false} onViewSource={noop} spanFullRow /></div>
}

export function NodeResourceCompact() {
  return <div style={wrap}><EvidenceCard group={nodeGroup} prominence="secondary" compact animateArrival={false} onViewSource={noop} spanFullRow /></div>
}
