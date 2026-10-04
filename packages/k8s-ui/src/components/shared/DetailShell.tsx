import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'

// Shared tabbed detail chrome. Hosts provide all data-aware pieces; this owns
// only the header, tab strip, optional controls, overlay, and content frame.
export interface DetailShellTab<TId extends string = string> {
  id: TId
  label: string
  icon?: ReactNode
  /** Trailing adornment after the label, e.g. an event count badge. */
  badge?: ReactNode
  /** Omit the tab from the strip without disturbing the others' identity. */
  hidden?: boolean
}

export interface DetailShellProps<TId extends string = string> {
  /**
   * A thin breadcrumb line above the identity header (the parent path —
   * ends at the current entity's parent, since the identity title already
   * names the entity). Hosted surfaces use this instead of `nav`.
   */
  breadcrumb?: ReactNode
  /** Inline leading control on the identity row — a back button in standalone Radar. */
  nav?: ReactNode
  identity: ReactNode
  headerActions?: ReactNode
  /** A line under the identity row, above the tabs — e.g. a status summary that holds on every tab. */
  subheader?: ReactNode
  scopeControls?: ReactNode
  tabs: DetailShellTab<TId>[]
  activeTab: TId
  onTabChange: (id: TId) => void
  tabStripEnd?: ReactNode
  overlay?: ReactNode
  /** Hide breadcrumb/identity/header actions when a host page already owns that chrome. */
  compactHeader?: boolean
  /**
   * Let the header actions wrap below the identity, as one group, when both
   * do not fit — for an identity that packs a lot onto its title line.
   */
  wrapHeader?: boolean
  children: ReactNode
}

export function DetailShell<TId extends string = string>({
  breadcrumb,
  nav,
  identity,
  headerActions,
  subheader,
  scopeControls,
  tabs,
  activeTab,
  onTabChange,
  tabStripEnd,
  overlay,
  compactHeader = false,
  wrapHeader = false,
  children,
}: DetailShellProps<TId>) {
  const visibleTabs = tabs.filter((t) => !t.hidden)
  const { stripRef, compact } = useCompactTabs(visibleTabs.map((t) => `${t.id}:${t.label}`).join('|'))

  return (
    <div className="flex flex-col h-full w-full bg-theme-base">
      {/* Header */}
      <div className="shrink-0 border-b border-theme-border bg-theme-base">
        {!compactHeader && (
          <>
            {breadcrumb && <div className="px-6 pt-2.5">{breadcrumb}</div>}
            <div className={clsx('px-6 flex items-start gap-4', wrapHeader && 'flex-wrap gap-y-2', breadcrumb ? 'pb-3 pt-1.5' : 'py-3')}>
              {nav}
              {/* Wrapping on, the identity's basis is its one-line width, so the
                  actions move below whenever the whole title line would not fit
                  beside them — never squeezing the name to keep them up. */}
              <div className={clsx('min-w-0', wrapHeader ? 'flex-auto' : 'flex-1')}>{identity}</div>
              {wrapHeader ? <div className="ml-auto flex shrink-0 items-start gap-4">{headerActions}</div> : headerActions}
            </div>
            {subheader && <div className="-mt-1 px-6 pb-3">{subheader}</div>}
          </>
        )}

        {/* Tabs (left) + scope controls / actions (right) */}
        <div className={clsx('flex items-center', compactHeader ? 'px-0' : 'border-t border-theme-border px-6')}>
          <div ref={stripRef} className={clsx('flex min-w-0 flex-1 overflow-x-auto', compact ? 'gap-0' : 'gap-1')} role="tablist">
            {visibleTabs.map((t) => (
              <DetailShellTabButton key={t.id} active={activeTab === t.id} compact={compact} onClick={() => onTabChange(t.id)}>
                {!compact && t.icon}
                {t.label}
                {t.badge}
              </DetailShellTabButton>
            ))}
          </div>
          {(scopeControls || tabStripEnd) && (
            <div className="ml-auto flex shrink-0 items-center gap-2 pl-2">
              {scopeControls}
              {tabStripEnd}
            </div>
          )}
        </div>
      </div>

      {overlay}

      {/* Tab content */}
      <div className="flex-1 overflow-hidden relative">{children}</div>
    </div>
  )
}

/**
 * Drops the tab icons and tightens spacing only while the full strip does not
 * fit, so a page with many tabs stays on one line at laptop widths and others
 * keep their look. The width the strip needs with icons is measured while it
 * shows them, so toggling cannot oscillate.
 */
function useCompactTabs(tabsKey: string) {
  const stripRef = useRef<HTMLDivElement>(null)
  const needed = useRef(0)
  const [compact, setCompact] = useState(false)
  useLayoutEffect(() => {
    needed.current = 0
    setCompact(false)
  }, [tabsKey])
  useLayoutEffect(() => {
    const el = stripRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const measure = () => {
      if (!compact) needed.current = el.scrollWidth
      const next = needed.current > el.clientWidth + 1
      setCompact((prev) => (prev === next ? prev : next))
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [compact, tabsKey])
  return { stripRef, compact }
}

function DetailShellTabButton({ active, compact, onClick, children }: { active: boolean; compact: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={clsx(
        'flex shrink-0 items-center gap-1.5 whitespace-nowrap py-2 text-sm font-medium border-b-2 transition-colors',
        compact ? 'px-2.5' : 'px-3',
        active
          ? 'text-theme-text-primary border-skyhook-500'
          : 'text-theme-text-secondary border-transparent hover:text-theme-text-primary hover:border-theme-border-light',
      )}
    >
      {children}
    </button>
  )
}
