import type { NamespacedRef } from './types'

export type ParsedRef = NamespacedRef

/**
 * Parse a `?a=` / `?b=` query value into `{namespace, name}`.
 * Cluster-scoped resources have no slash: `"my-cluster-role"` → `{namespace: "", name: "my-cluster-role"}`.
 * K8s names are DNS-1123 (no `/`) so splitting on the first slash is unambiguous.
 * Empty name is rejected — a URL like `?a=prod/` would otherwise wedge the
 * caller in an indefinite loading state since `useResource` has no name to fetch.
 */
export function parseRef(value: string | null | undefined): ParsedRef | null {
  if (!value) return null
  const slash = value.indexOf('/')
  if (slash < 0) {
    return { namespace: '', name: value }
  }
  const name = value.slice(slash + 1)
  if (!name) return null
  return { namespace: value.slice(0, slash), name }
}

/** Inverse of parseRef. Cluster-scoped emits just the name. */
export function refToParam(r: NamespacedRef): string {
  return r.namespace ? `${r.namespace}/${r.name}` : r.name
}
