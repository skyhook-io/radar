import { Facet } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const rail: CSSProperties = {
  width: 240,
  border: '1px solid var(--border-default)',
  borderRadius: 8,
  background: 'var(--bg-surface)',
  overflow: 'hidden',
}
const noop = () => {}

export function HealthFacet() {
  return (
    <div style={rail}>
      <Facet
        title="Health"
        selected={new Set(['degraded'])}
        onToggle={noop}
        options={[
          { value: 'healthy', label: 'Healthy', count: 142, tone: 'success' },
          { value: 'degraded', label: 'Degraded', count: 7, tone: 'warning' },
          { value: 'unhealthy', label: 'Unhealthy', count: 3, tone: 'error' },
          { value: 'unknown', label: 'Unknown', count: 2, tone: 'neutral' },
        ]}
      />
    </div>
  )
}

export function SyncFacet() {
  return (
    <div style={rail}>
      <Facet
        title="Sync Status"
        selected={new Set(['OutOfSync', 'Unknown'])}
        onToggle={noop}
        options={[
          { value: 'Synced', label: 'Synced', count: 38, tone: 'success' },
          { value: 'OutOfSync', label: 'OutOfSync', count: 5, tone: 'warning' },
          { value: 'Unknown', label: 'Unknown', count: 1, tone: 'info' },
        ]}
      />
    </div>
  )
}

export function MultipleFacets() {
  return (
    <div style={rail}>
      <Facet
        title="Namespace"
        selected={new Set(['production'])}
        onToggle={noop}
        options={[
          { value: 'production', label: 'production', count: 64, tone: 'neutral' },
          { value: 'staging', label: 'staging', count: 41, tone: 'neutral' },
          { value: 'kube-system', label: 'kube-system', count: 28, tone: 'neutral' },
          { value: 'monitoring', label: 'monitoring', count: 12, tone: 'neutral' },
        ]}
      />
      <Facet
        title="Source"
        selected={new Set(['argocd'])}
        onToggle={noop}
        options={[
          { value: 'argocd', label: 'Argo CD', count: 22, tone: 'info' },
          { value: 'flux', label: 'Flux', count: 9, tone: 'info' },
          { value: 'helm', label: 'Helm', count: 6, tone: 'neutral' },
        ]}
      />
    </div>
  )
}
