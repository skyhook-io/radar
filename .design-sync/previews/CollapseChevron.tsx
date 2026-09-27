import { CollapseChevron } from '@skyhook-io/k8s-ui'

const row = { display: 'flex', gap: 24, alignItems: 'center' } as const
const label = { display: 'flex', gap: 6, alignItems: 'center', fontSize: 13 } as const

export function ClosedAndOpen() {
  return (
    <div style={row}>
      <span style={label} className="text-theme-text-secondary">
        <CollapseChevron open={false} className="w-4 h-4" />
        Environment (12)
      </span>
      <span style={label} className="text-theme-text-secondary">
        <CollapseChevron open className="w-4 h-4" />
        Volume Mounts (3)
      </span>
    </div>
  )
}

export function Sizes() {
  return (
    <div style={row}>
      <span style={{ ...label, fontSize: 12 }} className="text-theme-text-secondary">
        <CollapseChevron open={false} className="h-3.5 w-3.5" />
        Advanced connection settings
      </span>
      <span style={label} className="text-theme-text-secondary">
        <CollapseChevron open={false} className="w-4 h-4" />
        Conditions
      </span>
    </div>
  )
}

export function InheritColorInWarningHeader() {
  return (
    <div
      style={{ width: 420, display: 'flex', alignItems: 'center', gap: 8, padding: '8px 12px', borderRadius: 6, fontSize: 13 }}
      className="bg-amber-500/10 border border-amber-500/30 text-amber-600 dark:text-amber-400"
    >
      <CollapseChevron open inheritColor className="w-4 h-4" />
      3 pods pending — insufficient cpu on NodePool general-purpose
    </div>
  )
}
