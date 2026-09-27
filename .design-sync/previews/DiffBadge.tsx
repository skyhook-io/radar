import { DiffBadge } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const row: CSSProperties = { display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }

export function Changes() {
  return (
    <div style={row}>
      <DiffBadge diff={{ summary: 'spec.replicas: 3 → 5', fields: [{ path: 'spec.replicas', oldValue: 3, newValue: 5 }] }} />
      <DiffBadge diff={{ summary: 'image updated', fields: [] }} />
      <DiffBadge diff={{ summary: '2 fields changed', fields: [] }} />
    </div>
  )
}

export function InEventCard() {
  const card: CSSProperties = {
    width: 360,
    padding: '10px 12px',
    border: '1px solid var(--border-default)',
    borderRadius: 8,
    background: 'var(--bg-surface)',
    display: 'flex',
    flexDirection: 'column',
    gap: 6,
  }
  const head: CSSProperties = { fontSize: 13, fontWeight: 500, color: 'var(--text-primary)' }
  const meta: CSSProperties = { fontSize: 11, color: 'var(--text-tertiary)' }
  return (
    <div style={card}>
      <div style={head}>Deployment/checkout updated</div>
      <div style={meta}>production · 2m ago</div>
      <div>
        <DiffBadge diff={{ summary: 'spec.template.spec.containers[0].image: v2.4.1 → v2.4.2', fields: [] }} />
      </div>
    </div>
  )
}
