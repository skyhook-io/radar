import { EventDetailPanel, type TimelineEvent } from '@skyhook-io/k8s-ui'

const T0 = Date.now() - 18 * 60_000
const at = (sec: number) => new Date(T0 + sec * 1000).toISOString()
const noop = () => {}
// The panel is a fixed bottom drawer; a transformed box becomes its containing block so it docks inside the cell.
const dock = { position: 'relative', width: 900, height: 300, transform: 'translateZ(0)', overflow: 'hidden' } as const

const rolloutUpdate: TimelineEvent = {
  id: 'dep-update',
  timestamp: at(0),
  source: 'informer',
  kind: 'Deployment',
  apiVersion: 'apps/v1',
  namespace: 'payments',
  name: 'checkout-api',
  eventType: 'update',
  healthState: 'degraded',
  diff: {
    summary: 'image v2.14.0 → v2.15.1, replicas 3 → 5',
    fields: [
      { path: 'spec.template.spec.containers[0].image', oldValue: 'ghcr.io/acme/checkout-api:v2.14.0', newValue: 'ghcr.io/acme/checkout-api:v2.15.1' },
      { path: 'spec.replicas', oldValue: 3, newValue: 5 },
    ],
  },
  labels: { app: 'checkout-api', 'app.kubernetes.io/part-of': 'payments' },
}

const rsAdd: TimelineEvent = {
  id: 'rs-add',
  timestamp: at(2),
  source: 'informer',
  kind: 'ReplicaSet',
  apiVersion: 'apps/v1',
  namespace: 'payments',
  name: 'checkout-api-6b8d7f9c4',
  eventType: 'add',
  owner: { kind: 'Deployment', name: 'checkout-api' },
}

const backoff: TimelineEvent = {
  id: 'pod-backoff',
  timestamp: at(95),
  source: 'k8s_event',
  kind: 'Pod',
  apiVersion: 'v1',
  namespace: 'payments',
  name: 'checkout-api-6b8d7f9c4-q7r2x',
  eventType: 'Warning',
  reason: 'BackOff',
  message: 'Back-off restarting failed container checkout-api in pod checkout-api-6b8d7f9c4-q7r2x_payments',
  count: 14,
  owner: { kind: 'ReplicaSet', name: 'checkout-api-6b8d7f9c4' },
}

const unhealthy: TimelineEvent = {
  id: 'pod-unhealthy',
  timestamp: at(70),
  source: 'k8s_event',
  kind: 'Pod',
  apiVersion: 'v1',
  namespace: 'payments',
  name: 'checkout-api-6b8d7f9c4-q7r2x',
  eventType: 'Warning',
  reason: 'Unhealthy',
  message: 'Readiness probe failed: HTTP probe failed with statuscode: 503',
  count: 6,
  owner: { kind: 'ReplicaSet', name: 'checkout-api-6b8d7f9c4' },
}

const scheduled: TimelineEvent = {
  id: 'pod-scheduled',
  timestamp: at(4),
  source: 'k8s_event',
  kind: 'Pod',
  apiVersion: 'v1',
  namespace: 'payments',
  name: 'checkout-api-6b8d7f9c4-q7r2x',
  eventType: 'Normal',
  reason: 'Scheduled',
  message: 'Successfully assigned payments/checkout-api-6b8d7f9c4-q7r2x to ip-10-0-42-17.ec2.internal',
}

const all = [rolloutUpdate, rsAdd, scheduled, unhealthy, backoff]

export function ChangeWithDiff() {
  return (
    <div style={dock}>
    <EventDetailPanel
      events={[rolloutUpdate]}
      selectedId={rolloutUpdate.id}
      allEvents={all}
      onSelectId={noop}
      onClose={noop}
      onResourceClick={noop}
    />
    </div>
  )
}

export function WarningEvent() {
  return (
    <div style={dock}>
    <EventDetailPanel
      events={[backoff]}
      selectedId={backoff.id}
      allEvents={all}
      onSelectId={noop}
      onClose={noop}
      onResourceClick={noop}
    />
    </div>
  )
}

export function ClusterOfEvents() {
  const cluster = [backoff, unhealthy, rolloutUpdate, rsAdd, scheduled]
  return (
    <div style={dock}>
    <EventDetailPanel
      events={cluster}
      selectedId={unhealthy.id}
      onSelectId={noop}
      onClose={noop}
      onResourceClick={noop}
    />
    </div>
  )
}
