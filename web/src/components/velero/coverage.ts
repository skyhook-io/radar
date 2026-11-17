import type { VeleroScheduleReading } from '@skyhook-io/k8s-ui'

/** Kubernetes' own namespaces (kube-system, kube-public, kube-node-lease and add-ons' kube-*), which backups usually leave out on purpose. */
const isKubernetesNamespace = (ns: string) => ns.startsWith('kube-')

/**
 * A namespace pattern as Velero matches it (gobwas/glob, no separators): `*`
 * any run, `?` one character, `{a,b}` alternatives, and a class: `[a-z]` is a
 * range only when it is exactly one (as `[!a-z]`); otherwise every character
 * between the brackets is listed as written, so `[ab-d]` is a, b, - and d.
 * A pattern that does not compile matches only itself.
 */
export function veleroGlobMatch(pattern: string, name: string): boolean {
  if (!/[*?[{]/.test(pattern)) return pattern === name
  let re = ''
  let depth = 0
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i]
    if (c === '*') re += '.*'
    else if (c === '?') re += '.'
    else if (c === '[') {
      const end = pattern.indexOf(']', i + 2)
      if (end < 0) return pattern === name
      let body = pattern.slice(i + 1, end)
      const negated = body.startsWith('!')
      if (negated) body = body.slice(1)
      const escape = (t: string) => t.replace(/[\\\]^-]/g, '\\$&')
      const range = body.length === 3 && body[1] === '-'
      re += `[${negated ? '^' : ''}${range ? `${escape(body[0])}-${escape(body[2])}` : escape(body)}]`
      i = end
    } else if (c === '{') {
      re += '(?:'
      depth++
    } else if (c === '}' && depth > 0) {
      re += ')'
      depth--
    } else if (c === ',' && depth > 0) re += '|'
    else re += c.replace(/[.+^$()|\\/\]{}]/g, '\\$&')
  }
  if (depth > 0) return pattern === name
  try {
    return new RegExp(`^${re}$`).test(name)
  } catch {
    return pattern === name
  }
}

/** Whether a schedule's template selects the namespace, as Velero reads the selection: exclusions win. */
export function scheduleIncludes(s: Pick<VeleroScheduleReading, 'includedNamespaces' | 'excludedNamespaces'>, namespace: string): boolean {
  if (s.excludedNamespaces.some((p) => veleroGlobMatch(p, namespace))) return false
  return s.includedNamespaces.length === 0 || s.includedNamespaces.includes('*') || s.includedNamespaces.some((p) => veleroGlobMatch(p, namespace))
}

/**
 * The namespaces no schedule selects, leaving out Kubernetes' own and the
 * namespaces Velero is installed in. A schedule counts whatever its state:
 * this answers what is declared, and a paused or failing schedule is said
 * elsewhere on the Overview.
 */
export function namespacesNoScheduleIncludes(namespaces: string[], schedules: VeleroScheduleReading[], installations: string[]): { uncovered: string[]; leftOut: number } {
  const own = new Set(installations)
  const listed = namespaces.filter((ns) => !isKubernetesNamespace(ns) && !own.has(ns))
  return {
    uncovered: listed.filter((ns) => !schedules.some((s) => scheduleIncludes(s, ns))).sort(),
    leftOut: namespaces.length - listed.length,
  }
}
