export interface ParsedRef {
  namespace: string
  name: string
}

/**
 * Parse a `?a=` / `?b=` query value into `{namespace, name}`.
 * Cluster-scoped resources have no slash: `"my-cluster-role"` → `{namespace: "", name: "my-cluster-role"}`.
 * K8s names are DNS-1123 (no `/`) so splitting on the first slash is unambiguous.
 */
export function parseRef(value: string | null | undefined): ParsedRef | null {
  if (!value) return null
  const slash = value.indexOf('/')
  if (slash < 0) {
    return { namespace: '', name: value }
  }
  return { namespace: value.slice(0, slash), name: value.slice(slash + 1) }
}

/** Inverse of parseRef. Cluster-scoped emits just the name. */
export function refToParam(r: ParsedRef): string {
  return r.namespace ? `${r.namespace}/${r.name}` : r.name
}
