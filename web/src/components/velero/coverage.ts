import type { VeleroScheduleReading } from '@skyhook-io/k8s-ui'

/** Kubernetes' own namespaces (kube-system, kube-public, kube-node-lease and add-ons' kube-*), which backups usually leave out on purpose. */
const isKubernetesNamespace = (ns: string) => ns.startsWith('kube-')

type GlobToken = { t: 'lit'; c: string } | { t: 'any' } | { t: 'star' } | { t: 'class'; chars: string; range?: [string, string] }

/**
 * The longest pattern Velero accepts: with its wildcards read as letters it
 * must be a namespace name, at most 63 characters.
 */
const VELERO_PATTERN_MAX = 63

// Characters Velero refuses in a namespace pattern (wildcard.ValidateNamespaceName
// and the namespace-name check), so a schedule using them is rejected and backs
// up nothing.
const VELERO_PATTERN_REFUSED = /[|(){},!\\]/

function parseGlob(pattern: string): GlobToken[] | null {
  const out: GlobToken[] = []
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i]
    if (c === '*') out.push({ t: 'star' })
    else if (c === '?') out.push({ t: 'any' })
    else if (c === '[') {
      const end = pattern.indexOf(']', i + 1)
      if (end <= i + 1) return null
      const body = pattern.slice(i + 1, end)
      if (body.length === 3 && body[1] === '-') {
        if (body[0] > body[2]) return null
        out.push({ t: 'class', chars: '', range: [body[0], body[2]] })
      } else out.push({ t: 'class', chars: body })
      i = end
    } else out.push({ t: 'lit', c })
  }
  return out
}

function tokenMatches(tok: GlobToken, ch: string): boolean {
  if (tok.t === 'any') return true
  if (tok.t === 'lit') return tok.c === ch
  if (tok.t !== 'class') return false
  return tok.range ? ch >= tok.range[0] && ch <= tok.range[1] : tok.chars.includes(ch)
}

// On a mismatch it resumes after the last star, never revisiting earlier
// ones: at most pattern × name steps, both at most 63 characters.
function matchTokens(tokens: GlobToken[], name: string): boolean {
  let p = 0
  let n = 0
  let starP = -1
  let starN = 0
  while (n < name.length) {
    const tok = tokens[p]
    if (tok?.t === 'star') {
      starP = p++
      starN = n
    } else if (tok && tokenMatches(tok, name[n])) {
      p++
      n++
    } else if (starP >= 0) {
      p = starP + 1
      n = ++starN
    } else return false
  }
  while (tokens[p]?.t === 'star') p++
  return p === tokens.length
}

/**
 * A namespace pattern as Velero matches it: `*` any run, `?` one character,
 * and a class, where `[a-z]` is a range only when it is exactly one;
 * otherwise every character between the brackets is listed as written, so
 * `[ab-d]` is a, b, - and d. A pattern Velero refuses (longer than a
 * namespace name, braces, `!`, an empty or descending class) matches only
 * itself. Patterns come from the cluster, so matching never goes through a
 * backtracking regular expression.
 */
export function veleroGlobMatch(pattern: string, name: string): boolean {
  if (!/[*?[]/.test(pattern)) return pattern === name
  const tokens = pattern.length <= VELERO_PATTERN_MAX && !VELERO_PATTERN_REFUSED.test(pattern) ? parseGlob(pattern) : null
  return tokens ? matchTokens(tokens, name) : pattern === name
}

// Velero v1.18.2 wildcard.validateBracePatterns: every [ closes, with something
// between, and no ] stands alone.
function bracketsValid(pattern: string): boolean {
  for (let i = 0; i < pattern.length; i++) {
    if (pattern[i] === '[') {
      const end = pattern.indexOf(']', i + 1)
      if (end < 0 || end === i + 1) return false
      i = end
    } else if (pattern[i] === ']') return false
  }
  return true
}

// Velero v1.18.2 collections.validateNamespaceName: "" is allowed (Velero's "no
// namespace"); wildcard.ValidateNamespaceName refuses |()!{}, and consecutive
// asterisks; then each of * ? [ ] read as a letter must leave a namespace name.
function validNamespacePattern(pattern: string): boolean {
  if (pattern === '') return true
  if (/[|()!{},]/.test(pattern) || pattern.includes('**') || !bracketsValid(pattern)) return false
  const asName = pattern.replace(/[*?[\]]/g, 'x')
  return asName.length <= VELERO_PATTERN_MAX && /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(asName)
}

/**
 * Why Velero refuses a schedule's namespace selection
 * (ValidateNamespaceIncludesExcludes), or undefined when it accepts it. Velero
 * then rejects the schedule, which backs up nothing.
 */
export function veleroSelectionProblem(s: Pick<VeleroScheduleReading, 'includedNamespaces' | 'excludedNamespaces'>): string | undefined {
  // Velero reads both lists as sets, so a repeated entry is one entry.
  const inc = [...new Set(s.includedNamespaces)].sort()
  const exc = [...new Set(s.excludedNamespaces)].sort()
  if (inc.includes('*') && inc.length > 1) return 'it includes “*” beside other namespaces'
  if (exc.includes('*')) return 'it excludes “*”'
  const both = exc.find((ns) => inc.includes(ns))
  if (both !== undefined) return `it both includes and excludes ${both}`
  const bad = [...inc, ...exc].find((p) => !validNamespacePattern(p))
  if (bad !== undefined) return `${bad} is not a namespace name or pattern Velero accepts`
  return undefined
}

/** Whether a schedule's template selects the namespace, as Velero reads the selection: exclusions win, and a selection Velero refuses selects nothing. */
export function scheduleIncludes(s: Pick<VeleroScheduleReading, 'includedNamespaces' | 'excludedNamespaces'>, namespace: string): boolean {
  if (veleroSelectionProblem(s)) return false
  if (s.excludedNamespaces.some((p) => veleroGlobMatch(p, namespace))) return false
  return s.includedNamespaces.length === 0 || s.includedNamespaces.includes('*') || s.includedNamespaces.some((p) => veleroGlobMatch(p, namespace))
}

/**
 * The namespaces no schedule selects, leaving out Kubernetes' own and the
 * namespaces Velero is installed in. A schedule counts whatever its state:
 * this answers what is declared, and a paused or failing schedule is said
 * elsewhere on the Overview.
 */
export function namespacesNoScheduleIncludes(
  namespaces: string[],
  schedules: VeleroScheduleReading[],
  installations: string[],
): { uncovered: string[]; leftOut: number } {
  const own = new Set(installations)
  const listed = namespaces.filter((ns) => !isKubernetesNamespace(ns) && !own.has(ns))
  return {
    uncovered: listed.filter((ns) => !schedules.some((s) => scheduleIncludes(s, ns))).sort(),
    leftOut: namespaces.length - listed.length,
  }
}
