import { ProblemAlerts } from '@skyhook-io/k8s-ui'

const wrap = { width: 440, padding: '10px 8px' }

export function Errors() {
  return (
    <div style={wrap}>
      <ProblemAlerts
        problems={[
          { color: 'red', message: 'Deployment does not have minimum availability — 2 of 5 replicas are unavailable.' },
        ]}
      />
    </div>
  )
}

export function Warnings() {
  return (
    <div style={wrap}>
      <ProblemAlerts
        problems={[
          { color: 'yellow', message: 'Application is OutOfSync — the live manifest differs from the desired state in Git.' },
        ]}
      />
    </div>
  )
}

export function Mixed() {
  return (
    <div style={wrap}>
      <ProblemAlerts
        problems={[
          { color: 'red', message: 'Health status is Degraded: readiness probe failed with HTTP 503.' },
          { color: 'yellow', message: 'Auto-sync is disabled; changes committed to Git will not be applied automatically.' },
        ]}
      />
    </div>
  )
}
