import { Fragment, useId, useLayoutEffect, useRef, useState } from 'react'
import type React from 'react'
import { GripVertical, Pin, PinOff, X } from 'lucide-react'
import { clsx } from 'clsx'
import { Tooltip } from '../ui/Tooltip'
import { isMac } from '../../utils/platform'
import { moveResourceColumn } from './column-pinning'
import type { Column } from './ResourcesView'

interface ColumnPickerListProps {
  /** Every offered column, in display order. */
  columns: Column[]
  visibleColumns: Set<string>
  customColumnKeys: Set<string>
  onToggle: (key: string) => void
  onRemoveCustom: (key: string) => void
  /** Receives every column key in display order, including hidden columns. */
  onReorder: (order: string[]) => void
  pinning: {
    pinned: readonly string[]
    paused: readonly string[]
    onToggle: (key: string) => void
  }
}

/** The Columns picker's rows: show/hide, remove a custom column, and reorder by
 *  dragging the grip or with Alt/Option+Up/Down on the row's checkbox. */
export function ColumnPickerList({
  columns,
  visibleColumns,
  customColumnKeys,
  onToggle,
  onRemoveCustom,
  onReorder,
  pinning,
}: ColumnPickerListProps) {
  const [dragKey, setDragKey] = useState<string | null>(null)
  // Insertion point among the movable rows: 0 = before the first one.
  const [dropGap, setDropGap] = useState<number | null>(null)
  const [announcement, setAnnouncement] = useState('')
  const hintId = useId()
  const mac = isMac()

  // React moves a keyed row by re-inserting DOM nodes, and re-inserting the
  // focused checkbox drops focus. Put it back so repeated presses keep working.
  const refocus = useRef<HTMLElement | null>(null)
  useLayoutEffect(() => {
    const el = refocus.current
    if (!el) return
    refocus.current = null
    if (el.isConnected && document.activeElement !== el) el.focus()
  })

  const commitMove = (key: string, toIndex: number) => {
    const next = moveResourceColumn(columns, pinning.pinned, key, toIndex)
    if (!next) return false
    onReorder(next)
    const col = columns.find(c => c.key === key)
    setAnnouncement(`${col?.label ?? key} moved to position ${next.indexOf(key) + 1} of ${columns.length}.`)
    return true
  }

  const handleKeyDown = (e: React.KeyboardEvent, key: string, movableIndex: number) => {
    if (!e.altKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return
    e.preventDefault()
    e.stopPropagation()
    const up = e.key === 'ArrowUp'
    refocus.current = e.target instanceof HTMLElement ? e.target : null
    if (!commitMove(key, movableIndex + (up ? -1 : 1))) {
      refocus.current = null
      const label = columns.find(c => c.key === key)?.label ?? key
      setAnnouncement(up ? `${label} is already at the top.` : `${label} is already at the bottom.`)
    }
  }

  // The gap above the row when the pointer is in its top half, else below it.
  const gapAt = (e: React.DragEvent, movableIndex: number) => {
    const rect = e.currentTarget.getBoundingClientRect()
    return e.clientY < rect.top + rect.height / 2 ? movableIndex : movableIndex + 1
  }

  const endDrag = () => {
    setDragKey(null)
    setDropGap(null)
  }

  return (
    <>
      <span id={hintId} className="sr-only">
        {mac ? 'Press Option+Up Arrow or Option+Down Arrow to move this column.' : 'Press Alt+Up Arrow or Alt+Down Arrow to move this column.'}
      </span>
      <span className="sr-only" role="status" aria-live="polite">{announcement}</span>
      {columns.map((col, i) => {
        const isCustom = customColumnKeys.has(col.key)
        const isPinned = pinning.pinned.includes(col.key)
        const isPaused = pinning.paused.includes(col.key)
        const startsGroup = i === 0 || isPinned !== pinning.pinned.includes(columns[i - 1].key)
        const sameDragGroup = dragKey !== null && pinning.pinned.includes(dragKey) === isPinned
        const fromIndex = dragKey ? columns.findIndex(c => c.key === dragKey) : -1
        const endsGroup = i === columns.length - 1 || isPinned !== pinning.pinned.includes(columns[i + 1].key)
        const showGap = (gap: number) => sameDragGroup && dropGap === gap && gap !== fromIndex && gap !== fromIndex + 1
        return (
          <Fragment key={col.key}>
            {startsGroup && (
              <div className={clsx('px-3 pt-2 pb-1 text-xs text-theme-text-tertiary', i > 0 && 'border-t border-theme-border mt-1')}>
                {isPinned ? 'Pinned' : 'Scrolling'}
              </div>
            )}
            <div
              data-column-row
              data-column-key={col.key}
              className={clsx(
                'group relative flex items-center gap-1.5 pl-1.5 pr-3 py-1.5 hover:bg-theme-elevated',
                dragKey === col.key && 'opacity-50',
              )}
              onKeyDown={e => handleKeyDown(e, col.key, i)}
              onDragOver={dragKey && sameDragGroup ? e => {
                e.preventDefault()
                e.dataTransfer.dropEffect = 'move'
                setDropGap(gapAt(e, i))
              } : undefined}
              onDrop={dragKey && sameDragGroup ? e => {
                e.preventDefault()
                const gap = gapAt(e, i)
                commitMove(dragKey, gap > fromIndex ? gap - 1 : gap)
                endDrag()
              } : undefined}
            >
              {showGap(i) && (
                <span aria-hidden className="pointer-events-none absolute inset-x-2 -top-px h-0.5 rounded-full bg-accent" />
              )}
              {showGap(i + 1) && endsGroup && (
                <span aria-hidden className="pointer-events-none absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-accent" />
              )}
              <Tooltip content={mac ? 'Drag to reorder, or press ⌥↑ / ⌥↓' : 'Drag to reorder, or press Alt+↑ / Alt+↓'} position="left">
                <span
                  aria-hidden
                  draggable
                  onDragStart={e => {
                    e.dataTransfer.effectAllowed = 'move'
                    // Firefox starts a drag only when data is set.
                    e.dataTransfer.setData('text/plain', col.key)
                    const row = e.currentTarget.closest('[data-column-row]')
                    if (row instanceof HTMLElement) e.dataTransfer.setDragImage(row, 12, row.offsetHeight / 2)
                    setDragKey(col.key)
                  }}
                  onDragEnd={endDrag}
                  className="flex shrink-0 cursor-grab items-center text-theme-text-tertiary opacity-60 group-hover:opacity-100 active:cursor-grabbing"
                >
                  <GripVertical className="w-3.5 h-3.5" />
                </span>
              </Tooltip>
              <label className="flex items-center gap-2 flex-1 min-w-0 cursor-pointer">
                <input
                  type="checkbox"
                  checked={visibleColumns.has(col.key)}
                  onChange={event => {
                    refocus.current = event.currentTarget
                    onToggle(col.key)
                  }}
                  disabled={col.key === 'name'}
                  aria-describedby={hintId}
                  className="rounded border-theme-border"
                />
                <span className={clsx(
                  'text-sm truncate',
                  col.key === 'name' ? 'text-theme-text-tertiary' : 'text-theme-text-primary'
                )} title={col.tooltip || col.label}>
                  {col.label}
                </span>
              </label>
              {isPaused && <span className="text-xs text-theme-text-tertiary">Scrolls</span>}
              <Tooltip content={isPaused ? 'Scrolls until there is room to keep it pinned. Click to unpin.' : isPinned ? 'Unpin column' : visibleColumns.has(col.key) ? 'Pin column to the left' : 'Show this column before pinning it'} position="left">
                <button
                  type="button"
                  aria-label={`${isPinned ? 'Unpin' : 'Pin'} ${col.label}`}
                  aria-pressed={isPinned}
                  aria-describedby={hintId}
                  disabled={!visibleColumns.has(col.key)}
                  onClick={event => {
                    refocus.current = event.currentTarget
                    pinning.onToggle(col.key)
                  }}
                  className={clsx('shrink-0 rounded p-1 hover:bg-theme-elevated disabled:opacity-25 disabled:cursor-not-allowed', isPinned ? 'text-skyhook-400' : 'text-theme-text-tertiary')}
                >
                  {isPaused ? <PinOff className="w-3.5 h-3.5" /> : <Pin className="w-3.5 h-3.5" />}
                </button>
              </Tooltip>
              {isCustom && (
                <button
                  type="button"
                  onClick={() => onRemoveCustom(col.key)}
                  className="shrink-0 p-0.5 text-theme-text-tertiary hover:text-red-500 opacity-0 group-hover:opacity-100 focus:opacity-100"
                  title="Remove column"
                >
                  <X className="w-3.5 h-3.5" />
                </button>
              )}
            </div>
          </Fragment>
        )
      })}
    </>
  )
}
