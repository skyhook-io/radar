import type { ReactNode } from 'react'
import { Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { useAuthMe } from '../../api/client'
import { useIsLocalDeployment } from '../../contexts/CapabilitiesContext'

const SETUP_DOCS_URL = 'https://github.com/skyhook-io/radar/blob/main/docs/in-cluster.md#opt-in-permissions'

type ReadIdentity = 'user' | 'kubeconfig' | 'serviceAccount'

// Helm keeps releases in Secrets, so a 403 on the list means whichever identity
// Radar read with can't list Secrets, and the fix depends on which one that was.
// With auth on Radar impersonates the user, so the grant has to go to them and a
// chart setting would change nothing. With auth off it reads as itself: the
// kubeconfig identity for a local binary, or the ServiceAccount for an
// in-cluster or Cloud pod, where the grant is a chart setting.
function useHelmReadIdentity(): ReadIdentity {
  const { data: authMe } = useAuthMe()
  const isLocal = useIsLocalDeployment()
  if (authMe?.authEnabled) return 'user'
  return isLocal ? 'kubeconfig' : 'serviceAccount'
}

interface Copy {
  title: string
  summary: string
  detail: ReactNode
  link?: { href: string; label: string }
}

function copyFor(identity: ReadIdentity): Copy {
  switch (identity) {
    case 'user':
      return {
        title: "You don't have access to Helm releases",
        summary: "Your Kubernetes identity can't list Secrets, where Helm stores releases.",
        detail: (
          <>
            <p className="text-sm mt-1 max-w-md">
              Helm stores releases as Secrets, and your Kubernetes identity can't list Secrets in the
              namespaces being shown.
            </p>
            <p className="text-sm mt-2 max-w-md">
              Ask whoever administers this cluster for list access to Secrets in the namespaces whose
              releases you need. Radar reads Helm with your identity, so the grant goes to you, not to
              Radar.
            </p>
          </>
        ),
      }
    case 'kubeconfig':
      return {
        title: "You don't have access to Helm releases",
        summary: "Your kubeconfig identity can't list Secrets, where Helm stores releases.",
        detail: (
          <>
            <p className="text-sm mt-1 max-w-md">
              Helm stores releases as Secrets, and the identity in your current kubeconfig context
              can't list Secrets in the namespaces being shown.
            </p>
            <p className="text-sm mt-2 max-w-md">
              Ask whoever administers this cluster for list access to Secrets in the namespaces whose
              releases you need, or switch to a context that already has it.
            </p>
          </>
        ),
      }
    case 'serviceAccount':
      return {
        title: "Helm view isn't enabled",
        summary: "Radar's ServiceAccount can't read Secrets, where Helm stores releases.",
        detail: (
          <>
            <p className="text-sm mt-1 max-w-md">
              Helm stores releases as Secrets, and Radar's ServiceAccount isn't allowed to read them.
            </p>
            <p className="text-sm mt-2 max-w-md">
              Enable authentication so each user reads with their own Kubernetes permissions, or set{' '}
              <code className="font-mono text-theme-text-secondary">rbac.secrets=true</code> in the
              Radar chart. That flag lets Radar read every Secret in the cluster, and without
              authentication anyone who can open Radar can reveal them.
            </p>
          </>
        ),
        link: { href: SETUP_DOCS_URL, label: 'Setup options and trade-offs' },
      }
  }
}

interface HelmRestrictedStateProps {
  /** Text-only variant for the dashboard card, which is itself a button and
   *  can't nest a link. */
  compact?: boolean
  className?: string
}

export function HelmRestrictedState({ compact, className }: HelmRestrictedStateProps) {
  const copy = copyFor(useHelmReadIdentity())

  if (compact) {
    return (
      <div className={clsx('flex flex-col items-center justify-center h-full py-4 text-center text-theme-text-tertiary', className)}>
        <Shield className="w-8 h-8 text-amber-400 mb-2" />
        <span className="text-xs font-medium text-theme-text-secondary">{copy.title}</span>
        <span className="text-[11px] mt-1 px-3">{copy.summary}</span>
      </div>
    )
  }

  return (
    <div className={clsx('flex flex-col items-center justify-center h-full px-6 text-center text-theme-text-tertiary', className)}>
      <Shield className="w-8 h-8 text-amber-400 mb-2" />
      <p className="text-theme-text-secondary font-medium">{copy.title}</p>
      {copy.detail}
      {copy.link && (
        <a
          href={copy.link.href}
          target="_blank"
          rel="noopener noreferrer"
          className="mt-3 text-sm text-accent hover:underline"
        >
          {copy.link.label}
        </a>
      )}
    </div>
  )
}
