import { useResources } from './client'

// useNamespaceQuotas fetches a namespace's ResourceQuota objects via
// /api/resources/resourcequotas?namespaces=<ns> (the `{items, coverage}`
// list envelope; useResources returns the items). Backs the
// NamespaceRenderer quota-usage section — quota saturation is otherwise
// surfaced nowhere in the UI, yet it's exactly why a namespace stops
// admitting new pods.
export function useNamespaceQuotas(namespace: string, enabled = true) {
  return useResources<any>('resourcequotas', namespace, undefined, { enabled: enabled && !!namespace })
}

// useNamespaceLimitRanges fetches a namespace's LimitRange objects the same
// way. Backs the NamespaceRenderer rules section and the contextual link on
// Pod/workload details — the defaults and constraints applied at admission
// are otherwise only visible by reading the object.
export function useNamespaceLimitRanges(namespace: string, enabled = true) {
  return useResources<any>('limitranges', namespace, undefined, { enabled: enabled && !!namespace })
}

export function podLimitRangeNames(limitRanges: any[] | undefined): string[] {
  return (limitRanges ?? [])
    .filter(lr => lr.spec.limits?.some((entry: { type: string }) => entry.type === 'Container' || entry.type === 'Pod'))
    .map(lr => lr.metadata.name)
}
