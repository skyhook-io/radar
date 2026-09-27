import { DrainPlanDialog, type DrainPlan, type DrainPlanPod } from '@skyhook-io/k8s-ui'

const NODE = 'ip-10-0-42-17.ec2.internal'
const noop = () => {}

const pod = (namespace: string, name: string, outcome: DrainPlanPod['outcome'], reason: string, extra: Partial<DrainPlanPod> = {}): DrainPlanPod => ({
  namespace, name, outcome, reason, emptyDir: false, pdbChecked: true, ...extra,
})

function plan(pods: DrainPlanPod[], options: Partial<DrainPlan['options']> = {}): DrainPlan {
  return {
    node: NODE,
    generatedAt: new Date(Date.now() - 20_000).toISOString(),
    estimate: true,
    options: { ignoreDaemonSets: true, deleteEmptyDirData: false, force: false, ...options },
    summary: {
      evict: pods.filter((p) => p.outcome === 'evict').length,
      skip: pods.filter((p) => p.outcome === 'skip').length,
      mayBlock: pods.filter((p) => p.outcome === 'may-block').length,
    },
    pods,
    pdbsEvaluated: true,
  }
}

const mixedPods = [
  pod('payments', 'checkout-api-7d9f8c-x2kqp', 'evict', 'managed by ReplicaSet checkout-api-7d9f8c; the controller reschedules it'),
  pod('payments', 'ledger-worker-5c8b4-9mdtw', 'evict', 'managed by ReplicaSet ledger-worker-5c8b4; the controller reschedules it'),
  pod('payments', 'payments-db-2', 'may-block', 'PodDisruptionBudget payments-db-pdb currently allows no disruptions; the eviction may be refused until the budget recovers', { pdb: 'payments-db-pdb' }),
  pod('observability', 'otel-collector-cache-6f7b9-qp4ls', 'skip', 'uses emptyDir volumes; their data is lost on eviction, enable the Delete emptyDir data option to evict', { emptyDir: true }),
  pod('kube-system', 'aws-node-8kz2v', 'skip', 'managed by a DaemonSet; the DaemonSet controller would recreate it on this node'),
  pod('default', 'debug-shell', 'skip', 'not managed by a controller; would be lost, enable force to evict anyway'),
]

export function Plan() {
  return (
    <DrainPlanDialog
      open
      nodeName={NODE}
      plan={plan(mixedPods)}
      loading={false}
      options={{ force: false, deleteEmptyDirData: false }}
      onOptionsChange={noop}
      onConfirm={noop}
      onClose={noop}
      isDraining={false}
      planSupported
      onRefreshPlan={noop}
    />
  )
}

export function EmptyDirAndForce() {
  const pods = [
    pod('payments', 'checkout-api-7d9f8c-x2kqp', 'evict', 'managed by ReplicaSet checkout-api-7d9f8c; the controller reschedules it'),
    pod('observability', 'otel-collector-cache-6f7b9-qp4ls', 'evict', 'managed by ReplicaSet otel-collector-cache-6f7b9; the controller reschedules it', { emptyDir: true }),
    pod('default', 'debug-shell', 'evict', 'not managed by a controller; evicted because force is set'),
    pod('kube-system', 'aws-node-8kz2v', 'skip', 'managed by a DaemonSet; the DaemonSet controller would recreate it on this node'),
  ]
  return (
    <DrainPlanDialog
      open
      nodeName={NODE}
      plan={plan(pods, { force: true, deleteEmptyDirData: true })}
      loading={false}
      options={{ force: true, deleteEmptyDirData: true }}
      onOptionsChange={noop}
      onConfirm={noop}
      onClose={noop}
      isDraining={false}
      planSupported
      onRefreshPlan={noop}
    />
  )
}

export function PlanFailed() {
  return (
    <DrainPlanDialog
      open
      nodeName={NODE}
      loading={false}
      error='nodes "ip-10-0-42-17.ec2.internal" is forbidden: User "dev@acme.io" cannot list pods at the cluster scope'
      options={{ force: false, deleteEmptyDirData: false }}
      onOptionsChange={noop}
      onConfirm={noop}
      onClose={noop}
      isDraining={false}
      planSupported
      onRefreshPlan={noop}
    />
  )
}
