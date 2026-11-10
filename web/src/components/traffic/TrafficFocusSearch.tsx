import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { clsx } from 'clsx'
import { Crosshair, X } from 'lucide-react'
import { Badge, Input } from '@skyhook-io/k8s-ui'
import { useNavCustomization } from '../../context/NavCustomization'
import { useRegisterShortcut } from '../../hooks/useKeyboardShortcuts'
import { formatRate, searchEndpoints, type EndpointSummary, type TrafficFocus } from './trafficFilters'

interface TrafficFocusSearchProps {
  endpoints: EndpointSummary[]
  onFocus: (focus: TrafficFocus) => void
  isRateBased: boolean
  /** The pane the overlay covers; the viewport when absent. */
  overlayContainer?: HTMLElement | null
  /** 'toolbar' is the floating chip over the graph; 'panel' the wider button
   *  in the too-large panel. Both open the same overlay. */
  variant?: 'toolbar' | 'panel'
}

export function TrafficFocusSearch({ endpoints, onFocus, isRateBased, overlayContainer, variant = 'toolbar' }: TrafficFocusSearchProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const resultsRef = useRef<HTMLDivElement>(null)

  const results = useMemo(() => (isOpen ? searchEndpoints(endpoints, query) : []), [isOpen, endpoints, query])

  useEffect(() => setSelectedIndex(0), [results])
  useEffect(() => {
    const el = resultsRef.current?.children[selectedIndex] as HTMLElement | undefined
    el?.scrollIntoView({ block: 'nearest' })
  }, [selectedIndex])

  const open = useCallback(() => {
    setIsOpen(true)
    setTimeout(() => inputRef.current?.focus(), 0)
  }, [])
  const close = useCallback(() => {
    setIsOpen(false)
    setQuery('')
  }, [])
  const choose = useCallback((e: EndpointSummary) => {
    onFocus({ namespace: e.namespace, name: e.name })
    close()
  }, [onFocus, close])

  // The view renders one of the two variants at a time, so whichever is shown
  // owns the shortcut. An embedding host (Radar Hub) binds / to its own search.
  const { embedded } = useNavCustomization()
  useRegisterShortcut({
    id: 'traffic-focus-search',
    keys: '/',
    description: 'Focus on a workload',
    category: 'Search',
    scope: 'traffic',
    handler: open,
    enabled: !embedded,
  })

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      close()
      return
    }
    if (results.length === 0) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelectedIndex(i => Math.min(i + 1, results.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelectedIndex(i => Math.max(i - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (results[selectedIndex]) choose(results[selectedIndex])
    }
  }

  const portalTarget = overlayContainer ?? (typeof document !== 'undefined' ? document.body : null)

  return (
    <>
      {variant === 'toolbar' ? (
        <button
          type="button"
          onClick={open}
          className="flex items-center gap-1.5 px-2 py-1 rounded-lg bg-theme-surface/90 backdrop-blur border border-theme-border text-[11px] text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          <Crosshair className="w-3 h-3" />
          Focus on a workload
          {!embedded && <kbd className="hidden sm:inline-flex items-center px-1 text-[10px] bg-theme-elevated rounded border border-theme-border-light">/</kbd>}
        </button>
      ) : (
        <button
          type="button"
          onClick={open}
          className="btn-brand-muted inline-flex items-center gap-1.5 text-xs px-2.5 py-1 rounded-md"
        >
          <Crosshair className="w-3.5 h-3.5" />
          Focus on a workload…
        </button>
      )}

      {isOpen && portalTarget && createPortal(
        <div className={clsx(overlayContainer ? 'absolute' : 'fixed', 'inset-0 z-50 flex items-start justify-center pt-[10vh]')}>
          <div className="absolute inset-0 bg-theme-base/60 backdrop-blur-sm" onClick={close} />
          <div role="dialog" aria-label="Focus on a workload" className="relative w-full max-w-lg mx-4 bg-theme-surface border border-theme-border rounded-xl shadow-theme-lg overflow-hidden">
            <div className="flex items-center gap-3 px-4 py-3 border-b border-theme-border">
              <Crosshair className="w-5 h-5 text-theme-text-secondary" />
              <Input
                ref={inputRef}
                value={query}
                onChange={e => setQuery(e.target.value)}
                onKeyDown={onKeyDown}
                placeholder="Workload, service or external name…"
                className="flex-1 bg-transparent text-theme-text-primary placeholder-theme-text-disabled outline-none text-sm"
                autoFocus
              />
              {query && (
                <button type="button" onClick={() => setQuery('')} aria-label="Clear" className="p-1 text-theme-text-secondary hover:text-theme-text-primary">
                  <X className="w-4 h-4" />
                </button>
              )}
              <kbd className="px-1.5 py-0.5 text-xs text-theme-text-tertiary bg-theme-elevated rounded border border-theme-border-light">ESC</kbd>
            </div>
            <div ref={resultsRef} className="max-h-[50vh] overflow-y-auto">
              {results.map((e, index) => (
                <button
                  key={e.id}
                  type="button"
                  onClick={() => choose(e)}
                  // Not onMouseEnter: results re-rendering under a resting
                  // pointer would move the highlight away from what was typed.
                  onMouseMove={() => { if (index !== selectedIndex) setSelectedIndex(index) }}
                  className={clsx(
                    'w-full flex items-center gap-3 px-4 py-2 text-left transition-colors',
                    index === selectedIndex ? 'bg-theme-elevated/60' : 'hover:bg-theme-elevated/30',
                  )}
                >
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium text-theme-text-primary truncate">{e.name}</span>
                      <span className="px-1.5 py-0.5 text-[10px] rounded bg-theme-elevated text-theme-text-secondary">{e.workloadKind || e.kind}</span>
                    </div>
                    {e.namespace && <div className="text-xs text-theme-text-tertiary truncate">{e.namespace}</div>}
                  </div>
                  <div className="flex items-center gap-1.5 shrink-0 text-[10px] tabular-nums">
                    {e.drops > 0 && <Badge severity="error" size="sm">{e.drops.toLocaleString()} dropped</Badge>}
                    {e.errors > 0 && <Badge severity="error" size="sm">{isRateBased ? `${formatRate(e.errors)}/s` : e.errors.toLocaleString()} 5xx</Badge>}
                  </div>
                </button>
              ))}
              {results.length === 0 && (
                <div className="px-4 py-6 text-center text-sm text-theme-text-tertiary">
                  {query
                    ? <>No traffic for &ldquo;{query}&rdquo; in this view</>
                    : 'Nothing in this view has traffic yet'}
                </div>
              )}
            </div>
            <div className="px-4 py-2 border-t border-theme-border text-xs text-theme-text-tertiary">
              Shows the workload and everything it talks to. Only endpoints with traffic in this view are listed.
            </div>
          </div>
        </div>,
        portalTarget,
      )}
    </>
  )
}
