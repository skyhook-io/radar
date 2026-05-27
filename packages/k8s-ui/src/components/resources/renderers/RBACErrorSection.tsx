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

// RBAC reverse-lookup fails for two expected, non-alarming reasons that should
// not render as red errors:
//   503 with an "RBAC cache not available" message — the identity Radar connects
//         with can't read RBAC, so the informers never synced (feature not
//         granted). A config state, not a failure. (A generic connectivity 503,
//         "Not connected to cluster", is a real fault and must NOT be downplayed
//         as an RBAC-grant problem — it falls through to the red branch.)
//   403 — the requesting user lacks list permission on bindings.
// Reserve the red treatment for genuine failures (connectivity / network / 500).
export function RBACErrorSection({
  title,
  error,
  icon = Shield,
  errorPrefix = 'Could not load permissions',
}: RBACErrorSectionProps) {
  const status = (error as { status?: number }).status

  if (status === 503 && error.message.includes('RBAC cache')) {
    return (
      <Section title={title} icon={icon}>
        <div className="text-sm text-theme-text-tertiary">
          RBAC visibility isn’t available — the identity Radar connects with can’t read
          RBAC resources in this cluster.
        </div>
      </Section>
    )
  }
  if (status === 403) {
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
