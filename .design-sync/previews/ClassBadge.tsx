import { ClassBadge } from '@skyhook-io/k8s-ui'

const row = { display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap', padding: 10 } as const

export function Classes() {
  return (
    <div style={row}>
      <ClassBadge workloadClass="service" />
      <ClassBadge workloadClass="worker" />
      <ClassBadge workloadClass="job" />
      <ClassBadge workloadClass="unknown" />
    </div>
  )
}

export function Mixed() {
  return (
    <div style={row}>
      <ClassBadge
        workloadClass="mixed"
        composition={[
          { cls: 'service', count: 2 },
          { cls: 'job', count: 3 },
        ]}
      />
    </div>
  )
}
