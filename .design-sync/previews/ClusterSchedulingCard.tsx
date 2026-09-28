import { ClusterSchedulingCard } from '@skyhook-io/k8s-ui'

type Certainty = 'exact' | 'lower_bound' | 'upper_bound' | 'unknown'

function obs(resources: Record<string, string>, certainty: Certainty = 'exact') {
  return {
    resources,
    certainty,
    sources: ['nodes', 'pods', 'nodeclaims'],
    asOf: '2026-09-28T09:41:00Z',
    granularity: 'aggregate' as const,
  }
}

const frame = { width: 760 } as const
const noop = () => {}

export function KarpenterFleetWithPendingDemand() {
  return (
    <div style={frame}>
      <ClusterSchedulingCard
        scope="karpenter"
        onExplain={noop}
        scheduling={{
          scheduledRequests: obs({ cpu: '21900m', memory: '79Gi', pods: '27' }),
          allocatable: obs({ cpu: '31200m', memory: '102Gi', pods: '220' }),
          inFlightCapacity: obs({ cpu: '4', memory: '16Gi' }),
        }}
        pending={obs({ cpu: '48750m', memory: '193Gi', 'nvidia.com/gpu': '8' })}
      />
    </div>
  )
}

export function ClusterScopeWithPreemptionVictims() {
  return (
    <div style={frame}>
      <ClusterSchedulingCard
        scope="cluster"
        onExplain={noop}
        scheduling={{
          scheduledRequests: obs({ cpu: '58400m', memory: '214Gi' }),
          allocatable: obs({ cpu: '94', memory: '356Gi' }),
          inFlightCapacity: obs({ cpu: '8', memory: '32Gi' }),
          negativePriorityRequests: obs({ cpu: '12', memory: '40Gi' }),
        }}
        pending={obs({ cpu: '0', memory: '0' })}
      />
    </div>
  )
}

export function PartialObservation() {
  return (
    <div style={frame}>
      <ClusterSchedulingCard
        scope="karpenter"
        onExplain={noop}
        scheduling={{
          scheduledRequests: obs({ cpu: '14200m', memory: '52Gi' }, 'lower_bound'),
          allocatable: obs({ cpu: '24', memory: '96Gi' }, 'lower_bound'),
          negativePriorityRequests: obs({ cpu: '2500m', memory: '6Gi' }, 'lower_bound'),
        }}
        pending={obs({ cpu: '6', memory: '24Gi' }, 'lower_bound')}
      />
    </div>
  )
}

export function NodeInventoryNotObserved() {
  return (
    <div style={frame}>
      <ClusterSchedulingCard
        scope="karpenter"
        onExplain={noop}
        scheduling={{
          scheduledRequests: obs({ cpu: '9', memory: '31Gi' }),
        }}
        pending={obs({ cpu: '3500m', memory: '12Gi' })}
      />
    </div>
  )
}

export function EmptyFleet() {
  return (
    <div style={frame}>
      <ClusterSchedulingCard
        scope="karpenter"
        onExplain={noop}
        scheduling={{
          scheduledRequests: obs({}),
          allocatable: obs({}),
        }}
        pending={obs({})}
      />
    </div>
  )
}
