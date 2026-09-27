import { HealthRing } from '@skyhook-io/k8s-ui'

const wrap = { display: 'flex', gap: 24, alignItems: 'center', padding: 8 }

export function Healthy() {
  return (
    <div style={wrap}>
      <HealthRing size={72} label="12" segments={[{ value: 12, color: '#22C55E' }]} />
    </div>
  )
}

export function Mixed() {
  return (
    <div style={wrap}>
      <HealthRing
        size={72}
        label="20"
        segments={[
          { value: 14, color: '#22C55E' },
          { value: 4, color: '#F59E0B' },
          { value: 2, color: '#EF4444' },
        ]}
      />
    </div>
  )
}

export function Empty() {
  return (
    <div style={wrap}>
      <HealthRing size={72} label="0" segments={[]} />
    </div>
  )
}
