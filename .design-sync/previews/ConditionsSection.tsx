import { ConditionsSection } from '@skyhook-io/k8s-ui'

const wrap = { width: 460, padding: 8 }
const ago = (mins: number) => new Date(Date.now() - mins * 60_000).toISOString()

export function Healthy() {
  return (
    <div style={wrap}>
      <ConditionsSection
        conditions={[
          { type: 'Available', status: 'True', reason: 'MinimumReplicasAvailable', message: 'Deployment has minimum availability.', lastTransitionTime: ago(184) },
          { type: 'Progressing', status: 'True', reason: 'NewReplicaSetAvailable', message: 'ReplicaSet "checkout-7d9f8c" has successfully progressed.', lastTransitionTime: ago(12) },
        ]}
      />
    </div>
  )
}

export function Failing() {
  return (
    <div style={wrap}>
      <ConditionsSection
        conditions={[
          { type: 'Available', status: 'False', reason: 'MinimumReplicasUnavailable', message: 'Deployment does not have minimum availability.', lastTransitionTime: ago(6) },
          { type: 'Progressing', status: 'False', reason: 'ProgressDeadlineExceeded', message: 'ReplicaSet "payments-api-5c8b" has timed out progressing.', lastTransitionTime: ago(4) },
          { type: 'ReplicaFailure', status: 'True', reason: 'FailedCreate', message: 'pods "payments-api-5c8b-" is forbidden: exceeded quota: compute-resources.', lastTransitionTime: ago(4) },
        ]}
      />
    </div>
  )
}

export function CustomTones() {
  return (
    <div style={wrap}>
      <ConditionsSection
        getConditionTone={(c) => (c.type === 'OutOfSync' ? 'warning' : undefined)}
        conditions={[
          { type: 'Healthy', status: 'True', reason: 'Healthy', message: 'All resources are healthy.', lastTransitionTime: ago(48) },
          { type: 'OutOfSync', status: 'True', reason: 'ComparisonResult', message: 'Live manifest differs from the desired state in Git.', lastTransitionTime: ago(9) },
          { type: 'Progressing', status: 'Unknown', reason: 'Reconciling', message: 'Waiting for the controller to report status.', lastTransitionTime: ago(2) },
        ]}
      />
    </div>
  )
}
