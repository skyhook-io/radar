import { Lock } from 'lucide-react'
import { useCloudRole, type CloudRole } from '../../api/client'

interface RoleGatedPanelProps {
  /** Minimum Cloud tier required to view the children. */
  min: CloudRole
  /** Human-readable name of the gated content, e.g. "release manifests". */
  feature: string
  children: React.ReactNode
}

/**
 * RoleGatedPanel wraps tab-content surfaces that require a Cloud role
 * (member+) to view. Renders the children when the gate passes; renders
 * an explanatory empty state when the caller's role is insufficient.
 *
 * Why a per-tab gate rather than hiding tabs from the list: viewers
 * should learn what the platform offers and what their tier can't do,
 * not have features silently vanish. This mirrors how the action
 * buttons (Uninstall, Upgrade, etc.) are rendered visible-but-disabled
 * with a role-aware tooltip.
 *
 * Bypasses for non-Cloud users (OSS, OIDC, etc.) — `canAtLeast` returns
 * true when no Cloud role is present. The backend gate has the same
 * shape, so the frontend stays in lockstep.
 */
export function RoleGatedPanel({ min, feature, children }: RoleGatedPanelProps) {
  const { role, canAtLeast } = useCloudRole()
  if (canAtLeast(min)) {
    return <>{children}</>
  }
  return (
    <div className="flex flex-col items-center justify-center h-full py-12 px-6 text-center">
      <div className="rounded-full bg-theme-surface p-3 mb-3">
        <Lock className="w-5 h-5 text-theme-text-tertiary" aria-hidden />
      </div>
      <div className="text-sm font-medium text-theme-text-primary">
        Your role can't view {feature}
      </div>
      <div className="mt-1 max-w-md text-xs text-theme-text-secondary">
        You're signed in as <span className="inline-code">{role ?? 'viewer'}</span>.
        This view requires <span className="inline-code">{min}</span> or higher.
        Ask a {min} or owner if you need access.
      </div>
    </div>
  )
}
