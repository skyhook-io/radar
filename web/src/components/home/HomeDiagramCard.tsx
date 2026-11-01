import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { ArrowRight } from 'lucide-react'

interface HomeDiagramCardProps {
  icon: LucideIcon
  title: string
  // One fact about this cluster, right-aligned in the header.
  fact?: ReactNode
  // The picture of the view behind the card. It fills the body.
  drawing: ReactNode
  // One sentence saying what the view shows, under the drawing.
  caption: ReactNode
  footer: string
  onNavigate: () => void
}

// The frame Home's Topology and Live Traffic cards share: the whole card opens
// the view, so the drawing and caption carry no controls of their own.
export function HomeDiagramCard({ icon: Icon, title, fact, drawing, caption, footer, onNavigate }: HomeDiagramCardProps) {
  return (
    <button
      type="button"
      onClick={onNavigate}
      className="group h-[260px] w-full rounded-xl bg-theme-surface shadow-theme-sm hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200 text-left overflow-hidden focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
    >
      <div className="flex flex-col h-full w-full">
        <div className="flex items-center justify-between gap-3 px-5 py-3 border-b border-theme-border/50">
          <div className="flex items-center gap-2 shrink-0">
            <Icon className="w-4 h-4 text-theme-text-tertiary" />
            <span className="text-xs font-semibold uppercase tracking-wider text-theme-text-secondary">{title}</span>
          </div>
          {fact && (
            <span className="min-w-0 truncate text-[11px] text-theme-text-tertiary tabular-nums">{fact}</span>
          )}
        </div>

        <div className="flex-1 min-h-0 flex flex-col px-3 pt-1.5 pb-2.5">
          <div className="flex-1 min-h-0">{drawing}</div>
          <p className="px-2 pt-1 text-[11.5px] leading-snug text-theme-text-secondary">{caption}</p>
        </div>

        <div className="px-4 py-1.5 border-t border-theme-border/50 flex items-center justify-end gap-1.5 text-[10px] font-semibold uppercase tracking-wider text-theme-text-secondary group-hover:text-theme-text-primary transition-colors">
          {footer}
          <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
        </div>
      </div>
    </button>
  )
}
