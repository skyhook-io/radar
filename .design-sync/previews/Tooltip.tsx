import { Tooltip } from '@skyhook-io/k8s-ui'
import { RefreshCw, Terminal } from 'lucide-react'
import { useEffect, useRef } from 'react'

const iconBtn = 'p-1.5 rounded-md text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover border border-theme-border bg-theme-surface'

function ShownOnMount({ children }: { children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>('button, [tabindex]')?.focus()
  }, [])
  return <div ref={ref}>{children}</div>
}

export function DisabledReasonShown() {
  return (
    <ShownOnMount>
      <div style={{ width: 460, height: 150, display: 'flex', alignItems: 'flex-end', justifyContent: 'center', paddingBottom: 24 }}>
        <Tooltip content="Draining requires patch nodes and create pods/eviction. Your role grants neither in this cluster." delay={0}>
          <button className="px-3 py-1.5 text-sm rounded-md border border-theme-border bg-theme-elevated text-theme-text-primary opacity-50 cursor-not-allowed">
            Drain node
          </button>
        </Tooltip>
      </div>
    </ShownOnMount>
  )
}

export function IconButtonTriggers() {
  return (
    <div style={{ display: 'flex', gap: 8 }}>
      <Tooltip content="Reconnect" delay={100} position="bottom">
        <button className={iconBtn} aria-label="Reconnect"><RefreshCw className="w-4 h-4" /></button>
      </Tooltip>
      <Tooltip content="Open local terminal">
        <button className={iconBtn} aria-label="Open local terminal"><Terminal className="w-4 h-4" /></button>
      </Tooltip>
    </div>
  )
}

export function HoverableValue() {
  return (
    <div style={{ fontSize: 13 }} className="text-theme-text-primary">
      Schedule:{' '}
      <Tooltip content="Daily at 2:00" position="right">
        <span className="font-mono border-b border-dotted border-theme-text-tertiary cursor-help">0 2 * * *</span>
      </Tooltip>
    </div>
  )
}
