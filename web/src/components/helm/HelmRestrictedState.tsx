import type { ReactNode } from 'react'
import { Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { useHelmReadIdentity, type HelmReadIdentity } from './helmReadIdentity'

const IN_CLUSTER_DOCS_URL = 'https://radarhq.io/docs/configuration/in-cluster#opt-in-permissions'
const CLOUD_RBAC_DOCS_URL = 'https://radarhq.io/docs/cloud/rbac'

interface Copy {
  title: string
  summary: string
  detail: ReactNode
  link?: { href: string; label: string }
}

function copyFor(identity: HelmReadIdentity): Copy {
  switch (identity) {
    case 'serviceAccount':
      return {
        title: 'The Helm view needs access to Secrets',
        summary: "Radar's ServiceAccount can't list Secrets, where Helm stores releases.",
        detail: (
          <>
            <p className="text-sm mt-1 max-w-md">
              Helm stores releases as Secrets, and Radar's ServiceAccount isn't allowed to list them.
              Whoever installed Radar can fix this in one of two ways.
            </p>
            <p className="text-sm mt-2 max-w-md">
              Turn on authentication. Radar then reads as each user, so people see releases only where
              their own Kubernetes access lets them list Secrets.
            </p>
            <p className="text-sm mt-2 max-w-md">
              Or set <code className="font-mono text-theme-text-secondary">rbac.secrets=true</code> in
              the Radar chart, or grant Radar's ServiceAccount list on Secrets if you manage its RBAC
              yourself. Without authentication, anyone who can open Radar can then read every Secret
              in the cluster, including any ServiceAccount token Secrets, so treat it like
              cluster-admin access.
            </p>
          </>
        ),
        link: { href: IN_CLUSTER_DOCS_URL, label: 'Compare the options' },
      }
    case 'user':
      return {
        title: "You can't see Helm releases",
        summary: "Your Kubernetes identity can't list Secrets, where Helm stores releases.",
        detail: (
          <>
            <p className="text-sm mt-1 max-w-md">
              Helm stores releases as Secrets, and your Kubernetes identity can't list Secrets in the
              namespaces shown.
            </p>
            <p className="text-sm mt-2 max-w-md">
              Ask whoever administers this cluster for list access to Secrets in the namespaces whose
              releases you need. Radar reads Helm as you, so the access has to be yours. It also lets
              you read what's in those Secrets.
            </p>
          </>
        ),
      }
    case 'cloudViewer':
      return {
        title: "You can't see Helm releases",
        summary: "Your Viewer role can't list Secrets, where Helm stores releases.",
        detail: (
          <>
            <p className="text-sm mt-1 max-w-md">
              Helm stores releases as Secrets, and your access to this cluster doesn't include listing
              Secrets in the namespaces shown.
            </p>
            <p className="text-sm mt-2 max-w-md">
              By default, the Viewer role can't read Secrets. Ask an owner of this organization for the
              Member role, or for list access to Secrets.
            </p>
          </>
        ),
        link: { href: CLOUD_RBAC_DOCS_URL, label: 'How roles map to cluster access' },
      }
    case 'cloudUser':
      return {
        title: "You can't see Helm releases",
        summary: "Your cluster access can't list Secrets, where Helm stores releases.",
        detail: (
          <>
            <p className="text-sm mt-1 max-w-md">
              Helm stores releases as Secrets, and your access to this cluster doesn't include listing
              Secrets in the namespaces shown.
            </p>
            <p className="text-sm mt-2 max-w-md">
              Whoever manages Radar's access to this cluster can grant your user or group list access
              to Secrets in the namespaces you need.
            </p>
          </>
        ),
        link: { href: CLOUD_RBAC_DOCS_URL, label: 'How roles map to cluster access' },
      }
    case 'kubeconfig':
      return {
        title: "You can't see Helm releases",
        summary: "Your kubeconfig identity can't list Secrets, where Helm stores releases.",
        detail: (
          <>
            <p className="text-sm mt-1 max-w-md">
              Helm stores releases as Secrets, and the identity in your current kubeconfig context can't
              list Secrets in the namespaces shown.
            </p>
            <p className="text-sm mt-2 max-w-md">
              Ask whoever administers this cluster for list access to Secrets in the namespaces whose
              releases you need, or switch to a context that has it.
            </p>
          </>
        ),
      }
    case 'pending':
    case 'unknown':
      return {
        title: "Can't list Helm releases",
        summary: "Radar can't list Secrets, where Helm stores releases.",
        detail: (
          <p className="text-sm mt-1 max-w-md">
            Helm stores releases as Secrets, and Radar wasn't allowed to list them. Radar couldn't tell
            which identity it read as, so it can't say which access is missing.
          </p>
        ),
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
      <div className={clsx('flex flex-col items-center justify-center h-full py-4 px-3 text-center text-theme-text-tertiary', className)}>
        <Shield className="w-8 h-8 text-amber-400 mb-2" />
        <span className="text-xs font-medium text-theme-text-secondary">Can't list Helm releases</span>
        <span className="text-[11px] mt-1">{copy.summary}</span>
      </div>
    )
  }

  return (
    <div className={clsx('flex flex-col items-center justify-center min-h-full px-6 py-6 text-center text-theme-text-tertiary', className)}>
      <Shield className="w-8 h-8 text-amber-400 mb-2" />
      <p className="text-theme-text-secondary font-medium">{copy.title}</p>
      {copy.detail}
      {copy.link && (
        <a
          href={copy.link.href}
          target="_blank"
          rel="noopener noreferrer"
          className="mt-3 text-sm text-accent-text hover:underline"
        >
          {copy.link.label}
        </a>
      )}
    </div>
  )
}
