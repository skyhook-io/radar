import type { ComponentType, ReactNode } from 'react'
import { ArrowLeft } from 'lucide-react'

// Standard view page header — icon + title + description, optional back button
// and right-aligned custom content (counts, tiles, settings, actions…). The
// consistent "where am I / what is this page" signal across views (Rancher
// Masthead / Headlamp SectionHeader pattern).
interface PageHeaderProps {
  icon: ComponentType<{ className?: string }>
  title: string
  description?: string
  /** Renders a back button to the left of the title when provided. */
  onBack?: () => void
  /** Right-aligned custom content — wraps onto multiple lines when it overflows
   *  (e.g. a row of count tiles). */
  actions?: ReactNode
}

export function PageHeader({ icon: Icon, title, description, onBack, actions }: PageHeaderProps) {
  return (
    <div className="flex items-center gap-4">
      {onBack && (
        <button
          onClick={onBack}
          aria-label="Back"
          className="p-1.5 rounded-lg hover:bg-theme-hover transition-colors shrink-0"
        >
          <ArrowLeft className="w-5 h-5 text-theme-text-secondary" />
        </button>
      )}
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <Icon className="w-5 h-5 text-theme-text-secondary shrink-0" />
          <h1 className="text-lg font-semibold text-theme-text-primary truncate">{title}</h1>
        </div>
        {description && (
          <p className="text-sm text-theme-text-tertiary mt-1 ml-7 truncate">{description}</p>
        )}
      </div>
      {actions && <div className="flex flex-wrap items-center justify-end gap-2 shrink-0">{actions}</div>}
    </div>
  )
}
