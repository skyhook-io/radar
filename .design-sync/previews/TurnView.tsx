import { TurnView } from '@skyhook-io/k8s-ui'

const wrap = { width: 560, padding: 8 }
const noop = () => {}
const POD = 'checkout-api-7d9f8c6b5-x2kqp'

const diagnoseTool = {
  kind: 'tool' as const, id: 'diag-1', tool: 'diagnose', status: 'done', ms: 1840, isError: false, radarEvidence: true,
  summary: JSON.stringify({ kind: 'deployment', namespace: 'payments', name: 'checkout-api' }),
  result: JSON.stringify({ pods: 3, crashCause: [{ container: 'checkout-api', reason: 'OOMKilled', exitCode: 137 }] }),
}
const logsTool = {
  kind: 'tool' as const, id: 'logs-1', tool: 'get_pod_logs', status: 'done', ms: 420, isError: false, radarEvidence: true,
  summary: JSON.stringify({ namespace: 'payments', name: POD, container: 'checkout-api', previous: true, tail_lines: 100 }),
  result: '2026-09-28T09:41:09Z WARN  c.a.checkout.cache.ProductCache - heap usage 238Mi / 256Mi during warm-up',
}
const nodeTool = {
  kind: 'tool' as const, id: 'node-1', tool: 'get_resource', status: 'running',
  summary: JSON.stringify({ kind: 'node', name: 'ip-10-0-3-41.ec2.internal' }),
}

export function Investigating() {
  return (
    <div style={wrap}>
      <TurnView
        agentLabel="Claude Code"
        turn={{
          status: 'running', diagnosis: null, error: null,
          startup: { phase: 'ready', model: 'claude-sonnet-5', toolCount: 24 },
          timeline: [
            { kind: 'thinking', text: 'The Deployment has 0/3 ready replicas. Starting with Radar’s diagnosis of the workload, then the previous container’s logs.' },
            diagnoseTool,
            { kind: 'thinking', text: 'All three pods terminate with **OOMKilled** (exit 137). Checking whether the node is under memory pressure before blaming the limit.' },
            logsTool,
            nodeTool,
          ],
        }}
      />
    </div>
  )
}

export function CompletedAssessment() {
  return (
    <div style={wrap}>
      <TurnView
        agentLabel="Claude Code"
        assessment
        hideConclusion
        onViewExplanation={noop}
        turn={{
          status: 'done', error: null,
          timeline: [
            { kind: 'thinking', text: 'Starting with Radar’s diagnosis of payments/checkout-api.' },
            diagnoseTool, logsTool, { ...nodeTool, status: 'done', ms: 210, isError: false, result: '{"kind":"Node"}' },
          ],
          diagnosis: { rootCause: 'checkout-api is OOMKilled at its 256Mi limit', summary: 'checkout-api is being OOMKilled during cache warm-up.', certainty: 'established', report: '', remediation: [] },
        }}
      />
    </div>
  )
}

export function FollowupQuestion() {
  return (
    <div style={wrap}>
      <TurnView
        agentLabel="Claude Code"
        turn={{
          status: 'done', error: null, actor: 'dana@acme.io',
          question: 'Would 384Mi be enough, or do we need 512Mi?',
          timeline: [{ kind: 'thinking', text: 'The warm-up peak was 238Mi of heap plus ~70Mi of metaspace and thread stacks.' }],
          diagnosis: {
            rootCause: '',
            report: '384Mi would leave about **75Mi** of headroom over the observed ~310Mi peak — enough today, but the product cache grows with the SKU count. **512Mi** keeps ~40% headroom and matches the `-Xmx384m` the JVM is already configured with.',
            remediation: [],
          },
        }}
      />
    </div>
  )
}

export function ApplyTurn() {
  return (
    <div style={wrap}>
      <TurnView
        agentLabel="Claude Code"
        onCheckStatus={noop}
        turn={{
          status: 'done', error: null, apply: true, applyOutcome: 'confirmed',
          question: 'Apply the recommended fix',
          timeline: [{
            kind: 'tool', id: 'patch-1', tool: 'patch_resource', status: 'done', ms: 380, isError: false, radarEvidence: true,
            summary: JSON.stringify({ kind: 'deployment', namespace: 'payments', name: 'checkout-api', patch: { spec: { template: { spec: { containers: [{ name: 'checkout-api', resources: { limits: { memory: '512Mi' } } }] } } } } }),
            result: 'deployment.apps/checkout-api patched',
          }],
          diagnosis: { rootCause: '', report: 'Patched `deployment/checkout-api`: memory limit on `checkout-api` is now **512Mi**. A new ReplicaSet is rolling out.', remediation: [] },
        }}
      />
    </div>
  )
}

export function FailedRun() {
  return (
    <div style={wrap}>
      <TurnView
        agentLabel="Codex"
        onRetryDiagnosis={noop}
        turn={{
          status: 'error', diagnosis: null,
          error: 'codex exited with status 1: stream disconnected before completion (rate limit reached for gpt-5-codex)',
          timeline: [diagnoseTool],
        }}
      />
    </div>
  )
}
