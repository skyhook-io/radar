import { ResultCard } from '@skyhook-io/k8s-ui'

const wrap = { width: 560, padding: 8 }
const noop = () => {}

const oomDiagnosis = {
  summary: 'checkout-api is being OOMKilled: its 256Mi memory limit is below what the JVM needs at startup.',
  certainty: 'established' as const,
  rootCause: 'Container `checkout-api` in `payments/checkout-api` exits with **OOMKilled** (exit 137) ~40s after each start; heap + metaspace settle near 310Mi against a 256Mi limit.',
  unresolved: ['Whether the 2.15.0 release raised baseline memory or the limit was always this tight — no metrics from before the rollout.'],
  report: 'The pods restart ~40s after each start.',
  steps: [
    { kind: 'mitigate' as const, text: 'Raise the memory limit to 512Mi:\n\n```\nkubectl -n payments set resources deployment/checkout-api -c checkout-api --limits=memory=512Mi\n```', precondition: 'the node pool has at least 512Mi allocatable per replica' },
    { kind: 'verify' as const, text: 'Watch the new ReplicaSet settle and confirm no further restarts:\n\n```\nkubectl -n payments rollout status deployment/checkout-api\n```' },
    { kind: 'investigate' as const, text: 'Compare `-Xmx` in `JAVA_TOOL_OPTIONS` against the container limit; the JVM should leave ~25% headroom for non-heap memory.' },
  ],
  remediation: [] as string[],
  recommendedIndex: 1,
  recommendedReason: 'Raising the limit stops the crash loop immediately without touching application code; it is reversible with one command.',
  confidence: 0.88,
}
oomDiagnosis.remediation = oomDiagnosis.steps.map((s) => s.text)

export function Conclusive() {
  return (
    <div style={wrap}>
      <ResultCard animate={false} diagnosis={oomDiagnosis} onApply={noop} compactActions explanation={{ status: 'idle', onGenerate: noop }} />
    </div>
  )
}

export function AllNextSteps() {
  return (
    <div style={wrap}>
      <ResultCard animate={false} diagnosis={oomDiagnosis} onApply={noop} section="actions" />
    </div>
  )
}

export function LegacyLikelyCause() {
  return (
    <div style={wrap}>
      <ResultCard
        animate={false}
        diagnosis={{
          rootCause: 'The image `ghcr.io/acme/checkout-api:v2.15.0` does not exist in the registry — the pods are stuck in **ImagePullBackOff** with `manifest unknown`.',
          report: 'Events on `checkout-api-7d9f8c6b5-x2kqp` show `Failed to pull image "ghcr.io/acme/checkout-api:v2.15.0": manifest unknown`. The previous ReplicaSet (v2.14.1) is still serving 2/3 replicas.',
          remediation: [
            'Confirm the CI pipeline published the tag: `crane ls ghcr.io/acme/checkout-api | grep v2.15`',
            'Roll back to the last working revision: `kubectl -n payments rollout undo deployment/checkout-api`',
          ],
          confidence: 0.74,
        }}
      />
    </div>
  )
}

export function EarlierAssessment() {
  return (
    <div style={wrap}>
      <ResultCard
        animate={false}
        readOnlyAssessment
        storyInline
        revisedAfter="Could the new Redis sidecar be using the memory instead?"
        diagnosis={{
          ...oomDiagnosis,
          summary: 'The redis-proxy sidecar is not the cause: checkout-api itself is OOMKilled at 256Mi.',
          certainty: 'likely' as const,
          unresolved: [],
        }}
        section="conclusion"
      />
    </div>
  )
}
