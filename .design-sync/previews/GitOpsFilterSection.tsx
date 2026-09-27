import { GitOpsFacetButton, GitOpsFilterSection } from '@skyhook-io/k8s-ui'
import { CircleCheck, CircleDot, HeartPulse } from 'lucide-react'
import type { CSSProperties } from 'react'

const noop = () => {}
const rail: CSSProperties = {
  width: 240,
  border: '1px solid var(--border-default, #D8E0EE)',
  borderRadius: 8,
  background: 'var(--bg-surface, #F8FBFE)',
  overflow: 'hidden',
}

export function SyncAndHealth() {
  return (
    <div style={rail}>
      <GitOpsFilterSection icon={CircleCheck} title="Sync">
        <GitOpsFacetButton label="Synced" count={38} tone="success" active onClick={noop} />
        <GitOpsFacetButton label="OutOfSync" count={5} tone="warning" active={false} onClick={noop} />
        <GitOpsFacetButton label="Reconciling" count={2} tone="info" active={false} onClick={noop} />
        <GitOpsFacetButton label="Unknown" count={1} active={false} onClick={noop} />
      </GitOpsFilterSection>
      <GitOpsFilterSection icon={HeartPulse} title="Health">
        <GitOpsFacetButton label="Healthy" count={41} tone="success" active={false} onClick={noop} />
        <GitOpsFacetButton label="Progressing" count={3} tone="info" active onClick={noop} />
        <GitOpsFacetButton label="Degraded" count={2} tone="error" active onClick={noop} />
        <GitOpsFacetButton label="Suspended" count={1} tone="warning" active={false} onClick={noop} />
      </GitOpsFilterSection>
    </div>
  )
}

export function Automation() {
  return (
    <div style={rail}>
      <GitOpsFilterSection
        icon={CircleDot}
        title="Automation (Sync policy)"
        info="Auto-sync applies Git changes automatically; manual apps need an explicit sync."
      >
        <GitOpsFacetButton label="Auto-sync" count={31} active onClick={noop} />
        <GitOpsFacetButton label="Manual" count={9} active={false} onClick={noop} />
        <GitOpsFacetButton label="Suspended" count={4} tone="warning" active={false} onClick={noop} />
      </GitOpsFilterSection>
    </div>
  )
}
