import { Shield } from 'lucide-react'
import type { ComponentType } from 'react'
import { Section } from '../../ui/drawer-components'

interface RBACErrorSectionProps {
  title: string
  error: Error
  // Matches the icon of the section's success/loading state (Shield for most,
  // Users for the Role bindings section) so the error state isn't jarring.
  icon?: ComponentType<{ className?: string }>
  // Prefix for the genuine-error line; copy differs slightly across renderers
  // ("permissions" on Pod/Workload, "RBAC data" on ServiceAccount).
  errorPrefix?: string
}

const errorStatus = (error: Error): number | undefined => (error as { status?: number }).status

// 503 because Radar's SA can't read RBAC, so the informers never synced — a
// cluster-static config state (same on every resource), not a failure. The message
// check distinguishes it from a generic connectivity 503 ("Not connected to
// cluster"), which is a real fault and must stay loud (red).
const isRBACCacheUnavailable = (error: Error): boolean =>
  errorStatus(error) === 503 && error.message.includes('RBAC cache')

// 403 because the requesting user lacks list permission on bindings.
const isRBACForbidden = (error: Error): boolean => errorStatus(error) === 403

// True for the two expected, non-actionable RBAC states above. Surfaces that treat
// the RBAC section as a bonus (Pod/Workload Permissions) hide it entirely for these.
// Genuine faults — connectivity 503, 500, network errors — are deliberately NOT
// included, so they still surface rather than being silently dropped.
export function isRBACUnavailable(error: Error): boolean {
  return isRBACForbidden(error) || isRBACCacheUnavailable(error)
}

// RBACErrorSection renders each expected state as a calm note (distinct copy per
// state) and reserves the red treatment for genuine failures. It shares the two
// sub-predicates with isRBACUnavailable so the "what counts as unavailable" rule
// has a single source of truth and can't drift.
export function RBACErrorSection({
  title,
  error,
  icon = Shield,
  errorPrefix = 'Could not load permissions',
}: RBACErrorSectionProps) {
  if (isRBACCacheUnavailable(error)) {
    return (
      <Section title={title} icon={icon}>
        <div className="text-sm text-theme-text-tertiary">
          RBAC visibility isn’t available — the identity Radar connects with can’t read
          RBAC resources in this cluster.
        </div>
      </Section>
    )
  }
  if (isRBACForbidden(error)) {
    return (
      <Section title={title} icon={icon}>
        <div className="text-sm text-theme-text-tertiary">
          You don’t have permission to view RBAC bindings here.
        </div>
      </Section>
    )
  }
  return (
    <Section title={title} icon={icon}>
      <div className="text-sm text-red-400">
        {errorPrefix}: {error.message}
      </div>
    </Section>
  )
}
