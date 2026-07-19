import { clsx } from 'clsx'
import { Link, useLocation, type To } from 'react-router-dom'

export function ChecksViewTabs() {
  const { pathname, search } = useLocation()
  const upgrade = pathname.startsWith('/checks/upgrade')
  const current = new URLSearchParams(search)
  const browsing = new URLSearchParams()
  for (const key of ['namespace', 'namespaces']) {
    const value = current.get(key)
    if (value) browsing.set(key, value)
  }
  const upgradeSearch = new URLSearchParams(browsing)
  const target = current.get('target')
  if (target) upgradeSearch.set('target', target)

  return (
    <nav className="flex items-center gap-1 border-b border-theme-border" aria-label="Check views">
      <ChecksTab to={{ pathname: '/checks', search: browsing.toString() }} active={!upgrade}>
        Best practices
      </ChecksTab>
      <ChecksTab to={{ pathname: '/checks/upgrade', search: upgradeSearch.toString() }} active={upgrade}>
        Upgrade impact
      </ChecksTab>
    </nav>
  )
}

function ChecksTab({ to, active, children }: { to: To; active: boolean; children: React.ReactNode }) {
  return (
    <Link
      to={to}
      aria-current={active ? 'page' : undefined}
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
