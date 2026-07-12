import { Link, useLocation } from 'react-router-dom'
import { clsx } from 'clsx'

export function CostViewTabs() {
  const { pathname } = useLocation()
  const rightsizing = pathname.startsWith('/cost/rightsizing')
  return (
    <div className="flex items-center gap-1 border-b border-theme-border" role="tablist" aria-label="Cost views">
      <CostTab to="/cost" active={!rightsizing}>Overview</CostTab>
      <CostTab to="/cost/rightsizing" active={rightsizing}>Rightsizing</CostTab>
    </div>
  )
}

function CostTab({ to, active, children }: { to: string; active: boolean; children: React.ReactNode }) {
  return (
    <Link
      to={to}
      role="tab"
      aria-selected={active}
      className={clsx(
        'relative px-3 py-2 text-sm font-medium transition-colors',
        active ? 'text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-secondary',
      )}
    >
      {children}
      {active && <span className="absolute inset-x-2 bottom-0 h-0.5 rounded-full bg-accent" />}
    </Link>
  )
}
