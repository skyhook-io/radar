import { useEffect, useRef, useState, type ReactNode } from 'react'
import { FolderTree, ShieldCheck, ChevronDown, Check } from 'lucide-react'
import { clsx } from 'clsx'
import type { TopologyMode, GroupingMode } from '../../types/core'
import { Tooltip } from '../ui/Tooltip'

interface TopologyControlsProps {
  viewMode: TopologyMode
  onViewModeChange: (mode: TopologyMode) => void
  groupingMode: GroupingMode
  onGroupingModeChange: (mode: GroupingMode) => void
  showNoGrouping?: boolean
  showPolicyEffect?: boolean
  onShowPolicyEffectChange?: (show: boolean) => void
  /** Show the "Fleet" button (CAPI cluster management view) */
  showFleetMode?: boolean
  /**
   * Navigate to the observed-traffic view. When provided, the "Network Flow"
   * tooltip offers a link to it — disambiguating the config-derived flow graph
   * here from the live, observed Traffic view. Omitted by hosts without one.
   */
  onNavigateToTraffic?: () => void
  /** Optional leading element (e.g. a freshness/liveness indicator). */
  leadingSlot?: ReactNode
}

export function TopologyControls({
  viewMode,
  onViewModeChange,
  groupingMode,
  onGroupingModeChange,
  showNoGrouping = true,
  showPolicyEffect = false,
  onShowPolicyEffectChange,
  showFleetMode = false,
  onNavigateToTraffic,
  leadingSlot,
}: TopologyControlsProps) {
  const [groupOpen, setGroupOpen] = useState(false)
  const groupRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!groupOpen) return
    const onDown = (e: MouseEvent) => {
      if (groupRef.current && !groupRef.current.contains(e.target as Node)) setGroupOpen(false)
    }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setGroupOpen(false) }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [groupOpen])

  const groupOptions: { value: GroupingMode; label: string }[] = [
    ...(showNoGrouping ? [{ value: 'none' as GroupingMode, label: 'No Grouping' }] : []),
    { value: 'namespace', label: 'By Namespace' },
    { value: 'app', label: 'By App Label' },
  ]
  const currentGroupLabel = groupOptions.find((o) => o.value === groupingMode)?.label ?? 'Grouping'

  return (
    <div className="absolute top-4 right-4 z-10 flex items-center gap-2">
      {/* Freshness/liveness status — backed for legibility over the canvas but
          borderless + divided off, so it reads as a status, not another control. */}
      {leadingSlot && (
        <>
          <div className="flex items-center rounded-lg bg-theme-surface/80 px-2.5 py-1.5 backdrop-blur">
            {leadingSlot}
          </div>
          <div className="h-5 w-px bg-theme-border/70" />
        </>
      )}
      {/* Policy effect toggle */}
      {onShowPolicyEffectChange && (
        <button
          onClick={() => onShowPolicyEffectChange(!showPolicyEffect)}
          className={`flex items-center gap-1.5 px-2.5 py-1.5 text-xs rounded-lg border transition-colors ${
            showPolicyEffect
              ? 'bg-indigo-600 text-white border-indigo-600'
              : 'bg-theme-surface/90 backdrop-blur text-theme-text-secondary border-theme-border hover:text-theme-text-primary'
          }`}
          title="Show NetworkPolicy effects on edges"
        >
          <ShieldCheck className="w-3.5 h-3.5" />
          Policies
        </button>
      )}

      {/* Grouping selector — themed dropdown (not a native <select>). */}
      <div ref={groupRef} className="relative">
        <button
          type="button"
          onClick={() => setGroupOpen((v) => !v)}
          aria-haspopup="menu"
          aria-expanded={groupOpen}
          className="flex items-center gap-1.5 px-2 py-1.5 bg-theme-surface/90 backdrop-blur border border-theme-border rounded-lg text-xs text-theme-text-primary hover:bg-theme-elevated transition-colors"
        >
          <FolderTree className="w-3.5 h-3.5 text-theme-text-secondary" />
          {currentGroupLabel}
          <ChevronDown className="w-3 h-3 text-theme-text-tertiary" />
        </button>
        {groupOpen && (
          <div role="menu" className="absolute right-0 top-full mt-1 z-50 min-w-[160px] rounded-lg border border-theme-border bg-theme-surface py-1 shadow-xl">
            {groupOptions.map((o) => (
              <button
                key={o.value}
                type="button"
                role="menuitemradio"
                aria-checked={groupingMode === o.value}
                onClick={() => { onGroupingModeChange(o.value); setGroupOpen(false) }}
                className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary transition-colors"
              >
                <Check className={clsx('w-3.5 h-3.5 shrink-0', groupingMode === o.value ? 'opacity-100 text-skyhook-500' : 'opacity-0')} />
                <span className="truncate">{o.label}</span>
              </button>
            ))}
          </div>
        )}
      </div>

      {/* View mode toggle */}
      <div className="flex items-center gap-0.5 p-1 bg-theme-surface/90 backdrop-blur border border-theme-border rounded-lg">
        <button
          onClick={() => onViewModeChange('resources')}
          className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
            viewMode === 'resources'
              ? 'bg-skyhook-600 text-white'
              : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
          }`}
        >
          Resources
        </button>
        <Tooltip
          position="bottom"
          content={
            <div className="space-y-1.5 text-left">
              <p className="text-theme-text-secondary">
                How requests <em>should</em> route, derived from Ingress, Services and
                routing CRDs (Traefik, Gateway API, Istio…) — not observed packets.
              </p>
              {onNavigateToTraffic && (
                <p className="text-theme-text-tertiary">
                  Looking for observed, measured traffic?{' '}
                  <button
                    type="button"
                    onClick={onNavigateToTraffic}
                    className="text-skyhook-400 hover:text-skyhook-300 underline underline-offset-2"
                  >
                    Open Live Traffic →
                  </button>
                </p>
              )}
            </div>
          }
        >
          <button
            onClick={() => onViewModeChange('traffic')}
            className={`px-2.5 py-1 text-xs rounded-md transition-colors whitespace-nowrap ${
              viewMode === 'traffic'
                ? 'bg-skyhook-600 text-white'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
            }`}
          >
            Network Flow <span className="opacity-70">(config)</span>
          </button>
        </Tooltip>
        {showFleetMode && (
          <button
            onClick={() => onViewModeChange('fleet')}
            className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
              viewMode === 'fleet'
                ? 'bg-skyhook-600 text-white'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
            }`}
            title="Cluster API fleet view — shows only CAPI resources and nodes"
          >
            Fleet
          </button>
        )}
      </div>
    </div>
  )
}
