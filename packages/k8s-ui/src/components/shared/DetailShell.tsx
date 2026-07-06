import type { ReactNode } from 'react'
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
  scopeControls?: ReactNode
  tabs: DetailShellTab<TId>[]
  activeTab: TId
  onTabChange: (id: TId) => void
  tabStripEnd?: ReactNode
  overlay?: ReactNode
  /** Hide breadcrumb/identity/header actions when a host page already owns that chrome. */
  compactHeader?: boolean
  children: ReactNode
}

export function DetailShell<TId extends string = string>({
  breadcrumb,
  nav,
  identity,
  headerActions,
  scopeControls,
  tabs,
  activeTab,
  onTabChange,
  tabStripEnd,
  overlay,
  compactHeader = false,
  children,
}: DetailShellProps<TId>) {
  const visibleTabs = tabs.filter((t) => !t.hidden)

  return (
    <div className="flex flex-col h-full w-full bg-theme-base">
      {/* Header */}
      <div className="shrink-0 border-b border-theme-border bg-theme-base">
        {!compactHeader && (
          <>
            {breadcrumb && <div className="px-6 pt-2.5">{breadcrumb}</div>}
            <div className={clsx('px-6 flex items-start gap-4', breadcrumb ? 'pb-3 pt-1.5' : 'py-3')}>
              {nav}
              <div className="flex-1 min-w-0">{identity}</div>
              {headerActions}
            </div>
          </>
        )}

        {/* Tabs (left) + scope controls / actions (right) */}
        <div className={clsx('flex items-center', compactHeader ? 'px-0' : 'border-t border-theme-border px-6')}>
          <div className="flex gap-1" role="tablist">
            {visibleTabs.map((t) => (
              <DetailShellTabButton key={t.id} active={activeTab === t.id} onClick={() => onTabChange(t.id)}>
                {t.icon}
                {t.label}
                {t.badge}
              </DetailShellTabButton>
            ))}
          </div>
          {(scopeControls || tabStripEnd) && (
            <div className="ml-auto flex items-center gap-2">
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

function DetailShellTabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={clsx(
        'flex items-center gap-1.5 px-3 py-2 text-sm font-medium border-b-2 transition-colors',
        active
          ? 'text-theme-text-primary border-skyhook-500'
          : 'text-theme-text-secondary border-transparent hover:text-theme-text-primary hover:border-theme-border-light',
      )}
    >
      {children}
    </button>
  )
}
