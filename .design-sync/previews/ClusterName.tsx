import { ClusterName } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const row: CSSProperties = { display: 'flex', flexDirection: 'column', gap: 14, alignItems: 'flex-start' }
const card: CSSProperties = { display: 'flex', gap: 12, flexWrap: 'wrap' }
const cell: CSSProperties = {
  width: 240,
  padding: '10px 12px',
  border: '1px solid var(--border-default)',
  borderRadius: 8,
  background: 'var(--bg-surface)',
}
const wheel: CSSProperties = { fontSize: 14, lineHeight: 1, color: 'var(--text-tertiary)' }

export function Providers() {
  return (
    <div style={row}>
      <div style={cell}>
        <ClusterName name="gke_skyhook-prod_us-east1_prod-cluster-us-east1" />
      </div>
      <div style={cell}>
        <ClusterName name="arn:aws:eks:us-west-2:123456789012:cluster/staging-payments" />
      </div>
      <div style={cell}>
        <ClusterName name="clusterUser_rg-platform_aks-prod-westeurope" />
      </div>
      <div style={cell}>
        <ClusterName name="kind-radar-gitops-demo" fallbackBadge={<span style={wheel}>⎈</span>} />
      </div>
    </div>
  )
}

export function Stacked() {
  return (
    <div style={card}>
      <div style={cell}>
        <ClusterName name="gke_skyhook-prod_us-east1_prod-cluster-us-east1" variant="stacked" />
      </div>
      <div style={cell}>
        <ClusterName name="arn:aws:eks:eu-central-1:987654321098:cluster/analytics-eu" variant="stacked" />
      </div>
    </div>
  )
}

export function NoBadge() {
  return (
    <div style={card}>
      <div style={cell}>
        <ClusterName name="gke_skyhook-prod_us-east1_prod-cluster-us-east1" noBadge />
      </div>
      <div style={cell}>
        <ClusterName name="minikube" noBadge />
      </div>
    </div>
  )
}
