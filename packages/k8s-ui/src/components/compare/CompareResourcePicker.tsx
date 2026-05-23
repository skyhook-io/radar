import { useEffect, useMemo, useRef, useState } from 'react'
import { clsx } from 'clsx'
import { GitCompare, Search, X } from 'lucide-react'
import { DialogPortal } from '../ui/DialogPortal'
import { pluralToKind } from '../../utils/navigation'
import type { CompareResourceRef } from './ResourceCompareView'
import { sortCandidates, filterCandidates } from './sort'
import { SIDE_TONES, type CompareSide } from './types'

export interface CompareResourcePickerProps {
  open: boolean
  onClose: () => void
  /** The resource the user is comparing *from*. */
  source: CompareResourceRef
  /** Which side the source occupies — drives the chip color/label in the dialog header.
   *  Defaults to 'a': the launcher flow's source becomes A in the resulting URL. */
  sourceSide?: CompareSide
  /** Candidate resources — same kind as source. Source is filtered out automatically. */
  candidates: CompareResourceRef[]
  loading?: boolean
  error?: unknown
  onPick: (r: CompareResourceRef) => void
}

export function CompareResourcePicker({
  open,
  onClose,
  source,
  sourceSide = 'a',
  candidates,
  loading,
  error,
  onPick,
}: CompareResourcePickerProps) {
  const [query, setQuery] = useState('')
  const [highlightIdx, setHighlightIdx] = useState(0)
  const listRef = useRef<HTMLUListElement | null>(null)

  // Reset query + highlight every time the picker opens. The drawer flow keeps
  // the picker mounted across opens, so without this a previous session's
  // search would leak into the next compare.
  useEffect(() => {
    if (open) {
      setQuery('')
      setHighlightIdx(0)
    }
  }, [open])

  const filtered = useMemo(
    () => filterCandidates(sortCandidates(candidates, source), query),
    [candidates, source, query],
  )

  // Clamp on any filter shape change — list length OR query (the user typing
  // can swap which rows are visible without changing length).
  useEffect(() => {
    setHighlightIdx(prev => (prev >= filtered.length ? 0 : prev))
  }, [filtered.length, query])

  useEffect(() => {
    if (!listRef.current) return
    const el = listRef.current.querySelector<HTMLElement>(`[data-idx="${highlightIdx}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [highlightIdx])

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    // Empty list: arrow keys would otherwise compute Math.min(0+1, -1) = -1.
    if (filtered.length === 0) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHighlightIdx(i => Math.min(i + 1, filtered.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHighlightIdx(i => Math.max(i - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const pick = filtered[highlightIdx]
      if (pick) onPick(pick)
    } else if (e.key === 'Home') {
      e.preventDefault()
      setHighlightIdx(0)
    } else if (e.key === 'End') {
      e.preventDefault()
      setHighlightIdx(filtered.length - 1)
    }
  }

  const sourceLabel = sourceSide === 'a' ? 'A' : 'B'
  const sourceChipBg = SIDE_TONES[sourceSide].chipBg

  return (
    <DialogPortal open={open} onClose={onClose} className="max-w-xl w-full max-h-[70vh] flex flex-col">
      <div className="flex items-center justify-between px-4 py-3 border-b border-theme-border shrink-0">
        <div className="flex items-center gap-2">
          <GitCompare className="w-5 h-5 text-skyhook-400" />
          <h3 className="text-sm font-semibold text-theme-text-primary">
            {sourceSide === 'a'
              ? `Compare to another ${pluralToKind(source.kind)}`
              : `Replace side B with another ${pluralToKind(source.kind)}`}
          </h3>
        </div>
        <button
          onClick={onClose}
          className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          aria-label="Close"
        >
          <X className="w-5 h-5" />
        </button>
      </div>

      <div className="px-4 py-3 border-b border-theme-border shrink-0 space-y-2">
        <div className="text-xs text-theme-text-secondary flex items-center gap-1.5 flex-wrap">
          <span className={clsx('inline-flex items-center justify-center w-4 h-4 rounded text-[10px] font-bold leading-none', sourceChipBg)}>
            {sourceLabel}
          </span>
          <span className="font-mono text-theme-text-primary">
            {source.namespace && <span className="opacity-60">{source.namespace}/</span>}
            {source.name}
          </span>
        </div>
        <div className="relative">
          <Search className="w-4 h-4 absolute left-3 top-1/2 -translate-y-1/2 text-theme-text-tertiary pointer-events-none" />
          <input
            autoFocus
            type="text"
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Search by name or namespace…  ↑↓ navigate, ↵ pick"
            className="w-full pl-9 pr-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-lg text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-400"
          />
        </div>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto">
        {loading && (
          <div className="flex items-center justify-center py-8 text-theme-text-secondary text-sm">
            Loading {source.kind}…
          </div>
        )}
        {error != null && (
          <div className="flex items-center justify-center py-8 text-red-400 text-sm">
            {error instanceof Error ? error.message : String(error)}
          </div>
        )}
        {!loading && error == null && filtered.length === 0 && (
          <div className="flex flex-col items-center justify-center py-12 text-theme-text-tertiary text-sm gap-1">
            {candidates.length <= 1
              ? `No other ${source.kind} available to compare against.`
              : 'No matches.'}
          </div>
        )}
        {!loading && error == null && filtered.length > 0 && (
          <ul ref={listRef} className="divide-y divide-theme-border/50">
            {filtered.map((c, idx) => {
              const sameNs = c.namespace === source.namespace
              const isActive = idx === highlightIdx
              return (
                <li key={`${c.namespace}/${c.name}`} data-idx={idx}>
                  <button
                    onClick={() => onPick(c)}
                    onMouseEnter={() => setHighlightIdx(idx)}
                    className={clsx(
                      'w-full text-left px-4 py-2.5 transition-colors',
                      'flex items-baseline gap-2',
                      isActive ? 'bg-skyhook-500/10' : 'hover:bg-theme-hover',
                    )}
                  >
                    <span className="text-sm font-mono text-theme-text-primary truncate flex-1">
                      {c.name}
                    </span>
                    {c.namespace && (
                      <span
                        className={clsx(
                          'text-xs font-mono shrink-0',
                          sameNs ? 'text-skyhook-400' : 'text-theme-text-tertiary',
                        )}
                        title={sameNs ? 'Same namespace as the source — likely target' : undefined}
                      >
                        {c.namespace}
                      </span>
                    )}
                  </button>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </DialogPortal>
  )
}
