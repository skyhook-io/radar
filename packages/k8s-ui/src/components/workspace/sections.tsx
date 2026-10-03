import { useEffect, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { toneTextClass } from '../ui/status-tone'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'

/** A folded section's one-line summary, and whether it opens on its own. */
export interface FoldSummary {
  text: string
  /** Something in the section needs a look: it opens itself. */
  attention: boolean
}

export function SectionHeading({ children, hint }: { children: ReactNode; hint?: ReactNode }) {
  return (
    <div className="mb-2 mt-5 flex items-baseline gap-2 first:mt-0">
      <h3 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{children}</h3>
      {hint && <span className="text-[11px] text-theme-text-tertiary">{hint}</span>}
    </div>
  )
}

/**
 * A section folded to one summary line. It opens itself when `attention`
 * turns true (data arriving after the first render included), and stays as
 * the reader left it otherwise.
 */
export function FoldSection({
  title,
  hint,
  summary,
  attention,
  children,
}: {
  title: ReactNode
  hint?: ReactNode
  summary: ReactNode
  attention: boolean
  children: ReactNode
}) {
  const [open, setOpen] = useState(attention)
  useEffect(() => {
    if (attention) setOpen(true)
  }, [attention])
  const d = useDisclosure(open)
  return (
    <div className="mt-5 first:mt-0">
      <button
        type="button"
        {...d.buttonProps}
        onClick={() => setOpen((v) => !v)}
        className="flex w-full flex-wrap items-baseline gap-x-2 gap-y-0.5 rounded text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        <CollapseChevron open={open} className="h-3 w-3 shrink-0 self-center text-theme-text-tertiary" />
        <h3 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{title}</h3>
        {hint && <span className="text-[11px] text-theme-text-tertiary">{hint}</span>}
        {!open && <span className={clsx('min-w-0 text-sm', attention ? toneTextClass('degraded') : 'text-theme-text-secondary')}>{summary}</span>}
      </button>
      <Collapse open={open} id={d.panelId}>
        <div className="pt-2">{children}</div>
      </Collapse>
    </div>
  )
}
