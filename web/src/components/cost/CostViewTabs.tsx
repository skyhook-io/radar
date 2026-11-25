import { isRightsizingScanPath } from '../../utils/page-links'
import { Link, useLocation } from 'react-router-dom'
import { RightsizingScanLink } from '../rightsizing/RightsizingScanLink'
import { clsx } from 'clsx'

export function CostViewTabs() {
  const { pathname, search } = useLocation()
  const rightsizing = isRightsizingScanPath(pathname)
  return (
    <div
      className="flex items-center gap-1 border-b border-theme-border"
      role="tablist"
      aria-label="Cost views"
    >
      <Link to={{ pathname: '/cost', search }} role="tab" aria-selected={!rightsizing} className={tabClass(!rightsizing)}>
        Overview
        {!rightsizing && <span className="absolute inset-x-2 bottom-0 h-0.5 rounded-full bg-accent" />}
      </Link>
      <RightsizingScanLink role="tab" aria-selected={rightsizing} className={tabClass(rightsizing)}>
        Rightsizing
        {rightsizing && <span className="absolute inset-x-2 bottom-0 h-0.5 rounded-full bg-accent" />}
      </RightsizingScanLink>
    </div>
  )
}

function tabClass(active: boolean) {
  return clsx(
    'relative px-3 py-2 text-sm font-medium transition-colors',
    active ? 'text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-secondary',
  )
}
