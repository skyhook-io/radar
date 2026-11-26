import { clsx } from 'clsx'
import { Layers } from 'lucide-react'
import { isClusterScopeWithheld, type ClusterScope } from '../../types/cluster-scope'

interface ClusterScopeNoteProps {
  /** The response's clusterScope; absent from older Radar. */
  scope?: ClusterScope | null
  /** What was withheld, e.g. "Recommendations withheld". */
  title?: string
  className?: string
}

/**
 * Says why a metrics view shows nothing when its series couldn't be kept to
 * this cluster in a shared metrics store. Renders nothing otherwise: a scoped
 * or single-cluster answer needs no note.
 */
export function ClusterScopeNote({ scope, title = 'Metrics withheld for this cluster', className }: ClusterScopeNoteProps) {
  if (!isClusterScopeWithheld(scope)) return null
  return (
    <div
      role="note"
      data-cluster-scope={scope.state}
      className={clsx(
        'flex items-start gap-2 rounded-lg border border-theme-border bg-theme-surface px-3 py-2 text-xs text-theme-text-secondary',
        className,
      )}
    >
      <Layers className="mt-0.5 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
      <div className="min-w-0">
        <p className="font-medium text-theme-text-primary">{title}</p>
        <p className="mt-0.5">{scope.note}</p>
      </div>
    </div>
  )
}
