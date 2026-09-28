import { RevisionHistory } from '@skyhook-io/k8s-ui'

const frame = { width: 640 } as const
const noop = () => {}

const hoursAgo = (h: number) => new Date(Date.now() - h * 3600_000).toISOString()

export function RecoveredFromFailedUpgrade() {
  return (
    <div style={frame}>
      <RevisionHistory
        currentRevision={14}
        history={[
          { revision: 14, status: 'deployed', chart: 'ingress-nginx-4.11.3', appVersion: '1.11.3', description: 'Rollback to 12', updated: hoursAgo(2) },
          { revision: 13, status: 'failed', chart: 'ingress-nginx-4.12.0', appVersion: '1.12.0', description: 'Upgrade "ingress-nginx" failed: context deadline exceeded', updated: hoursAgo(3) },
          { revision: 12, status: 'superseded', chart: 'ingress-nginx-4.11.3', appVersion: '1.11.3', description: 'Upgrade complete', updated: hoursAgo(26 * 24) },
          { revision: 11, status: 'superseded', chart: 'ingress-nginx-4.11.2', appVersion: '1.11.2', description: 'Upgrade complete', updated: hoursAgo(61 * 24) },
        ]}
        operations={[
          { kind: 'rollback', status: 'completed', source: 'helm_history', confidence: 'high', message: 'Rolled back to revision 12', revision: 14, rollbackRevision: 12 },
          { kind: 'upgrade_failed', status: 'rolled_back', source: 'helm_history', confidence: 'high', message: 'Upgrade to 4.12.0 failed', revision: 13, failedRevision: 13 },
        ]}
        onViewRevision={noop}
        onCompare={noop}
        onRollback={noop}
      />
    </div>
  )
}

export function ReadOnlyHistory() {
  return (
    <div style={frame}>
      <RevisionHistory
        currentRevision={7}
        history={[
          { revision: 7, status: 'deployed', chart: 'cert-manager-v1.15.3', appVersion: 'v1.15.3', description: 'Upgrade complete', updated: hoursAgo(5 * 24) },
          { revision: 6, status: 'superseded', chart: 'cert-manager-v1.15.1', appVersion: 'v1.15.1', description: 'Upgrade complete', updated: hoursAgo(40 * 24) },
          { revision: 5, status: 'superseded', chart: 'cert-manager-v1.14.5', appVersion: 'v1.14.5', description: 'Install complete', updated: hoursAgo(120 * 24) },
        ]}
        onViewRevision={noop}
        onCompare={noop}
      />
    </div>
  )
}

export function PendingUpgrade() {
  return (
    <div style={frame}>
      <RevisionHistory
        currentRevision={21}
        history={[
          { revision: 22, status: 'pending-upgrade', chart: 'kube-prometheus-stack-65.1.0', appVersion: 'v0.77.1', description: 'Preparing upgrade', updated: hoursAgo(0.4) },
          { revision: 21, status: 'deployed', chart: 'kube-prometheus-stack-62.7.0', appVersion: 'v0.76.1', description: 'Upgrade complete', updated: hoursAgo(9 * 24) },
        ]}
        operations={[
          { kind: 'pending', status: 'stuck_pending', source: 'helm_status', confidence: 'medium', message: 'Release has been pending-upgrade for 24m', revision: 22, pendingStatus: 'pending-upgrade' },
        ]}
        onViewRevision={noop}
        onCompare={noop}
        onRollback={noop}
      />
    </div>
  )
}

export function Empty() {
  return (
    <div style={frame}>
      <RevisionHistory currentRevision={0} history={[]} onViewRevision={noop} onCompare={noop} />
    </div>
  )
}
