import type { SelectedResource, ResourceRef } from '../types'

/**
 * Canonical callback type for navigating to a resource.
 * All components that trigger resource navigation should use this type.
 */
export type NavigateToResource = (resource: SelectedResource) => void

/**
 * Convert a singular kind (e.g., "Deployment") to plural API resource name (e.g., "deployments").
 * Single source of truth — uses English pluralization rules with a small alias map for
 * abbreviations and special mappings that aren't simple plurals.
 */
export function kindToPlural(kind: string): string {
  const kindLower = kind.toLowerCase()

  // Aliases: abbreviations or mappings to a different resource name
  const aliases: Record<string, string> = {
    horizontalpodautoscaler: 'horizontalpodautoscalers',
    pvc: 'persistentvolumeclaims',
    podgroup: 'pods',
  }
  if (aliases[kindLower]) return aliases[kindLower]

  // English pluralization rules (covers *Class→*classes, *Policy→*policies, *Repository→*repositories, etc.)
  if (kindLower.endsWith('s') || kindLower.endsWith('x') || kindLower.endsWith('ch') || kindLower.endsWith('sh')) {
    return kindLower + 'es'
  }
  if (kindLower.endsWith('y') && !/[aeiou]y$/.test(kindLower)) {
    return kindLower.slice(0, -1) + 'ies'
  }
  return kindLower + 's'
}

/**
 * Convert a ResourceRef (from backend relationships) to a SelectedResource (for navigation).
 * Handles kind singular→plural conversion.
 */
export function refToSelectedResource(ref: ResourceRef): SelectedResource {
  return {
    kind: kindToPlural(ref.kind),
    namespace: ref.namespace,
    name: ref.name,
    group: ref.group,
  }
}
