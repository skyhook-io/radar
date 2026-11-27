import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { clsx } from 'clsx'
import { ChevronDown } from 'lucide-react'
import { MultiSelectPicker } from '@skyhook-io/k8s-ui/components/ui/MultiSelectPicker'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '@skyhook-io/k8s-ui/utils/animation'
import type { VulnerabilityRiskFactor } from '../../api/vulnerabilities'
import { RISK_FACTORS, RISK_ORDER } from './parts'

const PANEL_WIDTH = 272

/** "Risk factors", or "Risk: Host port +1" once some are selected. */
export function riskFilterLabel(selected: VulnerabilityRiskFactor[]): string {
  const first = RISK_ORDER.find((f) => selected.includes(f))
  if (!first) return 'Risk factors'
  return `Risk: ${RISK_FACTORS[first].label}${selected.length > 1 ? ` +${selected.length - 1}` : ''}`
}

/**
 * The risk factors filter: one chip that opens a checklist, each factor with
 * how many workloads in scope carry it. A row is kept when one of its
 * workloads has any selected factor. The checklist is the shared
 * MultiSelectPicker, in the popover the timeline's Kinds filter uses.
 */
export function RiskFilter({ counts, selected, onChange }: {
  counts: Record<VulnerabilityRiskFactor, number>
  selected: VulnerabilityRiskFactor[]
  onChange: (next: VulnerabilityRiskFactor[]) => void
}) {
  const [open, setOpen] = useState(false)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, overlayExitMs('menu'))
  const [search, setSearch] = useState('')
  const rootRef = useRef<HTMLDivElement | null>(null)
  const panelRef = useRef<HTMLDivElement | null>(null)
  const [anchor, setAnchor] = useState<{ left: number; top: number } | null>(null)
  const byLabel = useMemo(() => new Map(RISK_ORDER.map((f) => [RISK_FACTORS[f].label, f])), [])
  const selectedLabels = useMemo(() => new Set(selected.map((f) => RISK_FACTORS[f].label)), [selected])
  const active = selected.length > 0

  useLayoutEffect(() => {
    if (!open) return
    const place = () => {
      const r = rootRef.current?.getBoundingClientRect()
      if (r) setAnchor({ left: Math.max(8, Math.min(r.left, window.innerWidth - PANEL_WIDTH - 8)), top: r.bottom + 4 })
    }
    place()
    window.addEventListener('scroll', place, true)
    window.addEventListener('resize', place)
    return () => {
      window.removeEventListener('scroll', place, true)
      window.removeEventListener('resize', place)
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    const onDown = (e: PointerEvent) => {
      const t = e.target as Node
      if (rootRef.current?.contains(t) || panelRef.current?.contains(t)) return
      setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); setOpen(false) }
    }
    window.addEventListener('pointerdown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const commit = (labels: Iterable<string>) => {
    const chosen = new Set([...labels].map((l) => byLabel.get(l)).filter((f): f is VulnerabilityRiskFactor => !!f))
    onChange(RISK_ORDER.filter((f) => chosen.has(f)))
  }

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        // The look of the chips beside it (FilterPill), with a chevron.
        className={clsx(
          'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs font-medium transition-colors',
          'focus-visible:ring-2 focus-visible:ring-theme-text-primary/20 focus-visible:outline-none',
          active || open
            ? 'bg-theme-text-primary/10 border-theme-text-primary/25 text-theme-text-primary'
            : 'border-theme-border-light text-theme-text-secondary hover:border-theme-border hover:text-theme-text-primary hover:bg-theme-hover/50',
        )}
      >
        <span>{riskFilterLabel(selected)}</span>
        <ChevronDown className="h-3.5 w-3.5 text-theme-text-tertiary" aria-hidden />
      </button>

      {shouldRender && anchor && createPortal(
        <div
          ref={panelRef}
          inert={!open}
          style={{ position: 'fixed', left: anchor.left, top: anchor.top, width: PANEL_WIDTH, ...overlayTransitionStyle(isOpen, 'menu') }}
          className={clsx(
            'z-50 origin-top-left rounded-md border border-theme-border bg-theme-surface shadow-theme-lg',
            TRANSITION_MENU,
            isOpen ? 'opacity-100 translate-y-0 scale-100' : 'opacity-0 -translate-y-1 scale-[0.97]',
            !open && 'pointer-events-none',
          )}
        >
          <MultiSelectPicker
            items={RISK_ORDER.map((f) => RISK_FACTORS[f].label)}
            selected={selectedLabels}
            onSelectionChange={commit}
            onClearAll={() => onChange([])}
            onDone={() => setOpen(false)}
            search={search}
            onSearchChange={setSearch}
            searchPlaceholder="Filter risk factors"
            summaryEmptyLabel="Any workload"
            noItemsLabel="No risk factors."
            clearAllDisabled={!active}
            clearAllAriaLabel="Clear risk factors"
            searchThreshold={RISK_ORDER.length}
            renderItemMeta={(label) => {
              const f = byLabel.get(label)
              return f ? <span className="shrink-0 text-xs tabular-nums text-theme-text-tertiary">({counts[f] ?? 0})</span> : null
            }}
          />
        </div>,
        document.body,
      )}
    </div>
  )
}
