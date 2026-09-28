import { InconclusiveCard } from '@skyhook-io/k8s-ui'

const wrap = { width: 540, padding: 8 }
const noop = () => {}

export function Plain() {
  return (
    <div style={wrap}>
      <InconclusiveCard
        animate={false}
        explanation={{ status: 'idle', onGenerate: noop }}
        diagnosis={{
          inconclusive: true,
          rootCause: '',
          report: 'Pod `checkout-api-7d9f8c6b5-x2kqp` restarted 4 times but the previous container logs were already rotated, and events older than 1h have expired. Nothing captured explains the exits.',
          remediation: [],
        }}
      />
    </div>
  )
}

export function StoryWithBlockers() {
  return (
    <div style={wrap}>
      <InconclusiveCard
        animate={false}
        diagnosis={{
          inconclusive: true,
          summary: 'Couldn’t tell why checkout-api exits with code 1 — the container prints nothing before it dies.',
          certainty: 'suspected',
          rootCause: '',
          report: 'The container exits with code 1 ~3s after start. [[radar:evidence=1]]',
          unresolved: [
            'The entrypoint writes no output before exiting, so logs cannot distinguish a config error from a crash.',
            'Whether the `payments-db` Secret rotated at 09:40 — Secret contents are not readable with the current role.',
          ],
          remediation: [],
        }}
        assessmentLimits={['Prometheus was not reachable, so memory and CPU history are missing.']}
      />
    </div>
  )
}
