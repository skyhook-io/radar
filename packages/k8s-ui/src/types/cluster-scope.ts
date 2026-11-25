/**
 * How a metrics view was kept to this cluster in a metrics store that may hold
 * several clusters' series. Mirrors `ClusterScope` in
 * internal/prometheus/view_scope.go. Older Radar omits it.
 */
export type ClusterScopeState =
  | 'scoped_configured'
  | 'single_cluster_asserted'
  | 'scoped_verified'
  | 'current_pod_uids'
  | 'unlabeled'
  | 'several_clusters'
  | 'label_missing'
  | 'unknown'

export interface ClusterScope {
  state: ClusterScopeState
  /** The cluster labels the decision checked, when there were any. */
  labels?: Record<string, string>
  /** Why, in a sentence the UI shows as is. */
  note: string
}

const WITHHELD_STATES: ReadonlySet<string> = new Set(['several_clusters', 'label_missing', 'unknown'])

/** The view's numbers were withheld because they couldn't be kept to this cluster. */
export function isClusterScopeWithheld(scope: ClusterScope | null | undefined): scope is ClusterScope {
  return scope != null && WITHHELD_STATES.has(scope.state)
}
