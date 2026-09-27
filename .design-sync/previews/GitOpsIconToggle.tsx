import { GitOpsIconToggle } from '@skyhook-io/k8s-ui'
import { LayoutGrid, List } from 'lucide-react'
import type { CSSProperties } from 'react'

const noop = () => {}
const segment: CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  overflow: 'hidden',
  borderRadius: 6,
  border: '1px solid var(--border-default, #D8E0EE)',
}
const label: CSSProperties = { fontSize: 11, color: 'var(--text-secondary, #555860)' }
const col: CSSProperties = { display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6 }

function ViewSwitch({ tiles }: { tiles: boolean }) {
  return (
    <div style={segment}>
      <GitOpsIconToggle active={!tiles} label="Table view" icon={List} onClick={noop} />
      <GitOpsIconToggle active={tiles} label="Tiles view" icon={LayoutGrid} onClick={noop} />
    </div>
  )
}

export function ViewModeToggle() {
  return (
    <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
      <ViewSwitch tiles={false} />
      <span style={label}>Table selected</span>
    </div>
  )
}

export function BothStates() {
  return (
    <div style={{ display: 'flex', gap: 32, alignItems: 'flex-start' }}>
      <div style={col}>
        <ViewSwitch tiles={false} />
        <span style={label}>Table active</span>
      </div>
      <div style={col}>
        <ViewSwitch tiles />
        <span style={label}>Tiles active</span>
      </div>
    </div>
  )
}
