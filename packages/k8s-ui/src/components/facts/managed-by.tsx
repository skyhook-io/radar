import type { ResourceRef } from '../../types/core'
import { gitOpsOwnerFromRef, type GitOpsOwnerRef } from '../../utils/gitops-owner'
import { RefLink, type NavigateToRef } from '../ui/RefLink'

function managerLabel(owner: GitOpsOwnerRef): string {
  if (owner.tool === 'argocd') return 'Argo CD application'
  return owner.kind === 'helmreleases' ? 'Flux HelmRelease' : 'Flux Kustomization'
}

/**
 * The GitOps object that manages a resource, from the server's manager
 * detection. Renders nothing for a manager that is not a GitOps controller,
 * and plain text when the manager's namespace is not recorded.
 */
export function ManagedByText({ refTo, onNavigate }: { refTo: ResourceRef; onNavigate?: NavigateToRef }) {
  const owner = gitOpsOwnerFromRef(refTo)
  if (!owner) return null
  return (
    <span>
      {managerLabel(owner)}{' '}
      {refTo.namespace ? (
        <RefLink refTo={refTo} onNavigate={onNavigate} mono>
          {`${refTo.namespace}/${refTo.name}`}
        </RefLink>
      ) : (
        <span className="font-mono">{refTo.name}</span>
      )}
    </span>
  )
}

/** The manager's label and name as text, for a table cell. */
export function managedByLabel(refTo: ResourceRef | undefined): string | undefined {
  const owner = refTo && gitOpsOwnerFromRef(refTo)
  return owner ? `${managerLabel(owner)} ${refTo!.namespace ? `${refTo!.namespace}/` : ''}${refTo!.name}` : undefined
}
