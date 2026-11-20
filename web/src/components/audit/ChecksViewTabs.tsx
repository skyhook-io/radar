import { clsx } from 'clsx'
import { Link, useLocation, type To } from 'react-router-dom'
import { useAPIResources } from '../../api/apiResources'
import { useCapabilities } from '../../api/client'
import { hasVulnerabilityReports } from '../../api/vulnerabilities'

export function ChecksViewTabs() {
  const { pathname, search } = useLocation()
  const { data: resources } = useAPIResources()
  const { data: capabilities } = useCapabilities()
  const upgrade = pathname.startsWith('/checks/upgrade')
  const vulnerabilities = pathname.startsWith('/checks/vulnerabilities')
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
      <ChecksTab to={{ pathname: '/checks', search: browsing.toString() }} active={!upgrade && !vulnerabilities}>
        Best practices
      </ChecksTab>
      <ChecksTab to={{ pathname: '/checks/upgrade', search: upgradeSearch.toString() }} active={upgrade}>
        Upgrade impact
      </ChecksTab>
      {(vulnerabilities || capabilities?.features?.runtimeAdvisoryChecks || hasVulnerabilityReports(resources)) && (
        <ChecksTab to={{ pathname: '/checks/vulnerabilities', search: browsing.toString() }} active={vulnerabilities}>
          Vulnerabilities
        </ChecksTab>
      )}
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
