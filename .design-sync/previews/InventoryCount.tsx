import { InventoryCount } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const table: CSSProperties = { width: 320, padding: 8, fontSize: 13 }
const rowStyle: CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'center',
  padding: '8px 10px',
  borderBottom: '1px solid var(--border-default)',
  color: 'var(--text-secondary)',
}
const last: CSSProperties = { ...rowStyle, borderBottom: 'none' }

export function HealthBreakdown() {
  return (
    <div style={table}>
      <div style={rowStyle}>
        <span>guestbook</span>
        <InventoryCount count={12} healthy={10} unhealthy={2} />
      </div>
      <div style={rowStyle}>
        <span>redis-ha</span>
        <InventoryCount count={8} healthy={8} unhealthy={0} />
      </div>
      <div style={last}>
        <span>ingress-nginx</span>
        <InventoryCount count={6} healthy={2} unhealthy={4} />
      </div>
    </div>
  )
}

export function PlainAndEmpty() {
  return (
    <div style={table}>
      <div style={rowStyle}>
        <span>cert-manager</span>
        <InventoryCount count={5} />
      </div>
      <div style={last}>
        <span>flux-system</span>
        <InventoryCount count={0} />
      </div>
    </div>
  )
}
