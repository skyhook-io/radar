import { HealthStatusBadge } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const row = { display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' } as const

export function AllStates() {
  return (
    <div style={row}>
      <HealthStatusBadge health="Healthy" />
      <HealthStatusBadge health="Progressing" />
      <HealthStatusBadge health="Degraded" />
      <HealthStatusBadge health="Suspended" />
      <HealthStatusBadge health="Missing" />
      <HealthStatusBadge health="Unknown" />
    </div>
  )
}

const table: CSSProperties = {
  width: 420,
  border: '1px solid var(--border-default, #D8E0EE)',
  borderRadius: 8,
  background: 'var(--bg-surface, #F8FBFE)',
}
const tr: CSSProperties = { display: 'flex', alignItems: 'center', gap: 12, padding: '8px 12px', borderTop: '1px solid var(--border-default, #D8E0EE)' }
const name: CSSProperties = { flex: 1, fontSize: 13, fontWeight: 500, color: 'var(--text-primary, #1A1C20)' }
const ns: CSSProperties = { fontSize: 11, color: 'var(--text-secondary, #555860)', width: 90 }

export function InApplicationsTable() {
  const rows = [
    { app: 'guestbook', ns: 'argocd', health: 'Healthy' as const },
    { app: 'cart-service', ns: 'argocd', health: 'Progressing' as const },
    { app: 'payment-gateway', ns: 'argocd', health: 'Degraded' as const },
    { app: 'legacy-billing', ns: 'argocd', health: 'Missing' as const },
  ]
  return (
    <div style={table}>
      {rows.map((r, i) => (
        <div key={r.app} style={i === 0 ? { ...tr, borderTop: 'none' } : tr}>
          <span style={name}>{r.app}</span>
          <span style={ns}>{r.ns}</span>
          <HealthStatusBadge health={r.health} />
        </div>
      ))}
    </div>
  )
}
