import { ToneDot } from '@skyhook-io/k8s-ui'

const row = { display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, padding: '3px 0' } as const

export function Tones() {
  return (
    <div style={{ padding: 12 }}>
      <div style={row}><ToneDot tone="healthy" /> payments-db-1 · primary</div>
      <div style={row}><ToneDot tone="degraded" /> payments-db-2 · lagging 48 MB</div>
      <div style={row}><ToneDot tone="alert" /> payments-db-3 · restarting</div>
      <div style={row}><ToneDot tone="unhealthy" /> payments-db-4 · CrashLoopBackOff</div>
      <div style={row}><ToneDot tone="unknown" /> payments-db-5 · status unread</div>
    </div>
  )
}
