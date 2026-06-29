import { Fragment, useEffect, useLayoutEffect, useRef, useState, type ComponentType } from 'react'
import { Loader2, MoreVertical } from 'lucide-react'
import { clsx } from 'clsx'
import { Tooltip } from './Tooltip'

export interface RowActionItem {
  key: string
  label: string
  icon: ComponentType<{ className?: string }>
  onClick: () => void
  disabled?: boolean
  disabledReason?: string
  pending?: boolean
  danger?: boolean
  /** Render a horizontal divider above this item. */
  divider?: boolean
}

interface RowActionMenuProps {
  items: RowActionItem[]
  ariaLabel?: string
  /** Compact button variant (default: true) — sized for table-row anchoring. */
  compact?: boolean
}

export function RowActionMenu({ items, ariaLabel = 'Row actions', compact = true }: RowActionMenuProps) {
  const [open, setOpen] = useState(false)
  // Flip the menu above the trigger when it would otherwise spill past the
  // viewport bottom. The GitOps table's bottom rows sit at the end of a scroll
  // container with the app's fixed overlay buttons below them, so a
  // downward-opening menu there clips its lowest items with no way to scroll
  // them into view. Measured after open (useLayoutEffect, pre-paint, no flicker).
  const [openUp, setOpenUp] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  useLayoutEffect(() => {
    if (!open) {
      setOpenUp(false)
      return
    }
    const trigger = ref.current?.getBoundingClientRect()
    const menuH = menuRef.current?.offsetHeight ?? 0
    if (!trigger) return
    const spaceBelow = window.innerHeight - trigger.bottom
    // Flip up only when there's not enough room below AND enough room above,
    // so a tall menu near the top doesn't get clipped at the other end.
    setOpenUp(menuH + 8 > spaceBelow && trigger.top > menuH + 8)
  }, [open])

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const triggerSize = compact ? 'p-1' : 'p-1.5'
  const iconSize = compact ? 'h-4 w-4' : 'h-5 w-5'

  return (
    <div ref={ref} className="relative inline-block">
      <button
        type="button"
        aria-label={ariaLabel}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={(e) => {
          e.stopPropagation()
          setOpen((v) => !v)
        }}
        className={clsx(
          'rounded text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary',
          triggerSize,
        )}
      >
        <MoreVertical className={iconSize} />
      </button>
      {open && (
        <div
          ref={menuRef}
          role="menu"
          className={clsx(
            'absolute right-0 z-50 min-w-[180px] rounded-lg border border-theme-border bg-theme-surface py-1 shadow-xl',
            openUp ? 'bottom-full mb-1' : 'top-full mt-1',
          )}
          onClick={(e) => e.stopPropagation()}
        >
          {items.map((item) => {
            const Icon = item.icon
            const content = (
              <button
                type="button"
                role="menuitem"
                disabled={item.disabled || item.pending}
                onClick={(e) => {
                  e.stopPropagation()
                  if (item.disabled || item.pending) return
                  item.onClick()
                  setOpen(false)
                }}
                className={clsx(
                  'flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors',
                  item.disabled || item.pending
                    ? 'cursor-not-allowed text-theme-text-tertiary'
                    : item.danger
                    ? 'text-red-500 hover:bg-theme-hover hover:text-red-400'
                    : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
                )}
              >
                {item.pending ? (
                  <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin" />
                ) : (
                  <Icon className="h-3.5 w-3.5 shrink-0" />
                )}
                <span className="truncate">{item.label}</span>
              </button>
            )
            return (
              <Fragment key={item.key}>
                {item.divider && <div className="my-1 h-px bg-theme-border" />}
                {item.disabled && item.disabledReason ? (
                  // wrapperClassName=w-full so the disabled item fills the menu
                  // like enabled items — the Tooltip wrapper is inline-flex and
                  // would otherwise shrink-wrap, and the menu inherits text-right
                  // from the table's actions cell, shoving the item to the edge.
                  <Tooltip content={item.disabledReason} position="left" wrapperClassName="w-full">
                    {content}
                  </Tooltip>
                ) : (
                  content
                )}
              </Fragment>
            )
          })}
        </div>
      )}
    </div>
  )
}
