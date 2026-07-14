interface TopologyBreadcrumbProps {
  /** The single active namespace to show. */
  namespace: string
  /** When set, renders a clickable "All Namespaces" crumb that clears the filter. */
  onClear?: () => void
}

/**
 * Namespace-context crumb for the topology overlay bar, shown when the graph is
 * scoped to a single namespace. Place it in the host's overlay-bar composition
 * (e.g. above the search) so it reads as page context.
 */
export function TopologyBreadcrumb({ namespace, onClear }: TopologyBreadcrumbProps) {
  return (
    <div className="flex items-center gap-1.5 pointer-events-auto">
      {onClear && (
        <button
          onClick={onClear}
          className="text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
        >
          All Namespaces
        </button>
      )}
      {onClear && <span className="text-xs text-theme-text-tertiary">/</span>}
      <span className="text-xs font-medium text-theme-text-secondary bg-theme-surface/80 backdrop-blur-sm border border-theme-border/50 rounded-md px-2 py-0.5">
        {namespace}
      </span>
    </div>
  )
}
