import { useState, type ReactNode } from 'react'
import { SlidersHorizontal } from 'lucide-react'
import { usePhoneLayout } from '../../hooks/usePhoneLayout'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'

// The header and scrolling body of a facet rail (Applications, GitOps). Below
// 900px the rail stacks above its list; on a phone that stack fills the first
// screen, so there the header becomes a "Filters" button with the active count
// and the facets open in place under it. Elsewhere it renders the plain
// "Filters" header over the body, as before.
export function FilterRailPanel({
  activeCount,
  clear,
  children,
}: {
  activeCount: number
  /** The Clear control for the header's right side; null hides it. */
  clear: ReactNode
  children: ReactNode
}) {
  const isPhone = usePhoneLayout()
  const [open, setOpen] = useState(false)
  const { panelId, buttonProps } = useDisclosure(open)

  if (!isPhone) {
    return (
      <>
        <div className="flex items-center justify-between border-b border-theme-border px-3 py-2">
          <span className="text-sm font-medium text-theme-text-secondary">Filters</span>
          {clear}
        </div>
        <div className="flex-1 overflow-y-auto">{children}</div>
      </>
    )
  }

  return (
    <>
      <div className="flex items-center justify-between gap-2 px-3 py-1.5">
        <button
          type="button"
          {...buttonProps}
          onClick={() => setOpen((v) => !v)}
          className="-ml-1 flex min-h-9 items-center gap-2 rounded-md px-1 text-sm font-medium text-theme-text-primary"
        >
          <SlidersHorizontal className="h-4 w-4 text-theme-text-secondary" aria-hidden />
          Filters
          {activeCount > 0 && (
            <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-skyhook-500 px-1 text-[10px] font-semibold leading-none text-white">
              {activeCount}
            </span>
          )}
          <CollapseChevron open={open} />
        </button>
        {clear}
      </div>
      <Collapse open={open} id={panelId}>
        <div className="max-h-56 overflow-y-auto border-t border-theme-border">{children}</div>
      </Collapse>
    </>
  )
}
