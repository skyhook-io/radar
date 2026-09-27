import { StatusDot } from '@skyhook-io/k8s-ui'
import type { ComponentProps } from 'react'

type Tone = ComponentProps<typeof StatusDot>['tone']

const col = { display: 'flex', flexDirection: 'column', gap: 10 } as const
const row = { display: 'flex', alignItems: 'center', gap: 20 }
const item = { display: 'flex', alignItems: 'center', gap: 8, fontSize: 13 }
const label = { fontSize: 13, color: 'var(--text-secondary)' }

const tones: [Tone, string][] = [
  ['healthy', 'kube-system · Running'],
  ['degraded', 'checkout-api · 2 restarts'],
  ['alert', 'payments-worker · Back-off restarting'],
  ['unhealthy', 'redis-cache · CrashLoopBackOff'],
  ['neutral', 'staging · 14 pods'],
  ['unknown', 'legacy-batch · Terminating'],
]

export function Tones() {
  return (
    <div style={col}>
      {tones.map(([tone, text]) => (
        <span key={tone} style={item}>
          <StatusDot tone={tone} />
          <span style={label}>{text}</span>
        </span>
      ))}
    </div>
  )
}

export function Sizes() {
  return (
    <div style={row}>
      {(['xs', 'sm', 'md'] as const).map((size) => (
        <span key={size} style={item}>
          <StatusDot tone="healthy" size={size} />
          <span style={label}>{size}</span>
        </span>
      ))}
    </div>
  )
}
