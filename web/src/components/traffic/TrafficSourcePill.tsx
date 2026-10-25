import { useEffect, useRef, useState } from 'react'
import { clsx } from 'clsx'
import { ChevronDown, Loader2 } from 'lucide-react'
import { StatusDot, toneTextClass } from '@skyhook-io/k8s-ui'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '../../utils/animation'

export type TrafficSourceConnectionState = 'connecting' | 'error' | 'connected'

interface TrafficSourcePillProps {
  sources: string[]
  active: string
  connection: TrafficSourceConnectionState
  onSwitch: (name: string) => void
  onRetry: () => void
}

// The picker is shown for every connection state except connecting: a source
// that cannot connect is the case where the user most needs to pick another.
export function TrafficSourcePill({ sources, active, connection, onSwitch, onRetry }: TrafficSourcePillProps) {
  const [open, setOpen] = useState(false)
  const pickerRef = useRef<HTMLDivElement>(null)
  const canPick = sources.length > 1
  const menuOpen = open && canPick && connection !== 'connecting'
  const { shouldRender, isOpen } = useAnimatedUnmount(menuOpen, overlayExitMs('menu'))

  // Capture phase so the handler runs before ReactFlow swallows the event.
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (pickerRef.current && !pickerRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handler, true)
    return () => document.removeEventListener('mousedown', handler, true)
  }, [open])

  const choose = (name: string) => {
    setOpen(false)
    if (name !== active) onSwitch(name)
  }

  // Keyboard activation of retry fires no outside mousedown, so the menu has to
  // be closed here or it would reappear once the attempt finishes.
  const retry = () => {
    setOpen(false)
    onRetry()
  }

  return (
    <div className="flex items-center gap-1.5 px-2 py-1 rounded-lg bg-theme-surface/90 backdrop-blur border border-theme-border text-[11px]">
      {connection === 'connecting' ? (
        <>
          <Loader2 className="h-3 w-3 animate-spin text-accent" />
          <span className="text-accent">Connecting...</span>
        </>
      ) : (
        <>
          <StatusDot tone={connection === 'error' ? 'degraded' : 'healthy'} size="md" />
          {canPick ? (
            <div className="relative" ref={pickerRef}>
              <button
                type="button"
                onClick={() => setOpen(!open)}
                aria-haspopup="listbox"
                aria-expanded={menuOpen}
                className="flex items-center gap-1 text-theme-text-secondary hover:text-theme-text-primary"
              >
                {active} <ChevronDown className="h-3 w-3" />
              </button>
              {shouldRender && (
                <div
                  role="listbox"
                  inert={!menuOpen || undefined}
                  className={clsx(
                    'absolute top-full left-0 mt-1 z-50 bg-theme-surface border border-theme-border rounded-md shadow-theme-lg py-1 min-w-[120px] origin-top-left',
                    TRANSITION_MENU,
                    isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-[0.97]',
                    !menuOpen && 'pointer-events-none',
                  )}
                  style={overlayTransitionStyle(isOpen, 'menu')}
                >
                  {sources.map(name => (
                    <button
                      key={name}
                      type="button"
                      role="option"
                      aria-selected={name === active}
                      onClick={() => choose(name)}
                      className={clsx('w-full text-left px-3 py-1 text-xs hover:bg-theme-hover capitalize', name === active && 'text-accent')}
                    >
                      {name}
                    </button>
                  ))}
                </div>
              )}
            </div>
          ) : (
            <span className="text-theme-text-secondary">{active}</span>
          )}
          {connection === 'error' && (
            <button type="button" onClick={retry} className={clsx('font-medium hover:underline', toneTextClass('degraded'))}>
              retry
            </button>
          )}
        </>
      )}
    </div>
  )
}
