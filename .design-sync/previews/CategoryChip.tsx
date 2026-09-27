import { CategoryChip } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const row: CSSProperties = { display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }
const line: CSSProperties = { display: 'flex', gap: 8, alignItems: 'center' }
const name: CSSProperties = { fontSize: 13, fontWeight: 500, color: 'var(--text-primary)' }

export function Categories() {
  return (
    <div style={row}>
      <CategoryChip category="addon" addonReason="cert-manager is a cluster add-on, not an application workload." />
      <CategoryChip category="mixed" />
    </div>
  )
}

export function InContext() {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div style={line}>
        <span style={name}>ingress-nginx</span>
        <CategoryChip category="addon" addonReason="Detected as a platform add-on from its Helm chart labels." />
      </div>
      <div style={line}>
        <span style={name}>checkout</span>
        <CategoryChip category="mixed" />
      </div>
    </div>
  )
}
