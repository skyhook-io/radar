import { ClusterSwitcher } from '@skyhook-io/k8s-ui'
import type { ClusterSwitcherItem } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const items: ClusterSwitcherItem[] = [
  { id: 'prod', name: 'gke_skyhook-prod_us-east1_prod-cluster', status: 'healthy', group: { key: 'gcp', label: 'Google Cloud' } },
  { id: 'staging', name: 'gke_skyhook-nonprod_us-east1_staging-cluster', status: 'healthy', group: { key: 'gcp', label: 'Google Cloud' } },
  { id: 'eks', name: 'arn:aws:eks:us-west-2:481923:cluster/data-platform', status: 'degraded', group: { key: 'aws', label: 'AWS' } },
  { id: 'kind', name: 'kind-radar-gitops-demo', secondary: 'local', status: 'neutral', group: { key: 'local', label: 'Local' } },
]

const wrap: CSSProperties = { display: 'flex', gap: 16, flexWrap: 'wrap', alignItems: 'center' }
const scopeBox: CSSProperties = {
  display: 'inline-flex',
  alignItems: 'stretch',
  height: 34,
  borderRadius: 6,
  border: '1px solid var(--border-default, #D8E0EE)',
  background: 'var(--bg-surface, #F8FBFE)',
  overflow: 'hidden',
}

export function Chip() {
  return (
    <div style={wrap}>
      <ClusterSwitcher currentId="prod" currentName={items[0].name} items={items} />
    </div>
  )
}

export function Segment() {
  return (
    <div style={scopeBox}>
      <ClusterSwitcher variant="segment" label="Cluster" currentId="prod" currentName={items[0].name} items={items} />
    </div>
  )
}

export function Loading() {
  return (
    <div style={wrap}>
      <ClusterSwitcher loading currentId="prod" currentName={items[0].name} items={items} />
    </div>
  )
}
