import type { ResourceRef } from '../types/core'

/**
 * resourceKey mirrors Go `audit.ResourceKey(group, kind, namespace, name)`:
 * `group|Kind|namespace|name`. Group first because group and namespace can each
 * independently be empty; `|` is delimiter-safe (K8s API groups follow
 * DNS-subdomain rules and can't contain it).
 */
export function resourceKey(group: string, kind: string, namespace: string, name: string): string {
  return `${group}|${kind}|${namespace}|${name}`;
}

export function resourceRefKey(ref: ResourceRef): string {
  return resourceKey(ref.group ?? '', ref.kind, ref.namespace, ref.name);
}

export function dedupeResourceRefs(refs: ResourceRef[]): ResourceRef[] {
  const seen = new Set<string>()
  return refs.filter(ref => {
    const key = resourceRefKey(ref)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

export function omitResourceRefs(refs: ResourceRef[], excluded: ResourceRef[]): ResourceRef[] {
  const excludedKeys = new Set(excluded.map(resourceRefKey))
  return refs.filter(ref => !excludedKeys.has(resourceRefKey(ref)))
}
