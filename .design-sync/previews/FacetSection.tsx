import { FacetButton, FacetSection } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const rail: CSSProperties = {
  width: 240,
  border: '1px solid var(--border-default)',
  borderRadius: 8,
  background: 'var(--bg-surface)',
  overflow: 'hidden',
}
const noop = () => {}

export function Namespaces() {
  return (
    <div style={rail}>
      <FacetSection title="Namespace">
        <FacetButton label="production" count={64} active onClick={noop} />
        <FacetButton label="staging" count={41} active={false} onClick={noop} />
        <FacetButton label="kube-system" count={28} active={false} onClick={noop} />
        <FacetButton label="monitoring" count={12} active={false} onClick={noop} />
      </FacetSection>
    </div>
  )
}

export function TonedStatuses() {
  return (
    <div style={rail}>
      <FacetSection title="Sync Status" info="Whether live state matches the desired manifests in Git.">
        <FacetButton label="Synced" count={38} tone="success" active onClick={noop} />
        <FacetButton label="OutOfSync" count={5} tone="warning" active={false} onClick={noop} />
        <FacetButton label="Missing" count={2} tone="error" active={false} onClick={noop} />
        <FacetButton label="Unknown" count={1} tone="info" active={false} onClick={noop} />
      </FacetSection>
    </div>
  )
}
