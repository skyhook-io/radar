import type { VeleroScheduleReading } from '@skyhook-io/k8s-ui'

/** Kubernetes' own namespaces (kube-system, kube-public, kube-node-lease and add-ons' kube-*), which backups usually leave out on purpose. */
const isKubernetesNamespace = (ns: string) => ns.startsWith('kube-')

type GlobToken = { t: 'lit'; c: string } | { t: 'any' } | { t: 'star' } | { t: 'class'; negated: boolean; chars: string; range?: [string, string] }
type GlobNode = GlobToken | { t: 'alt'; options: GlobNode[][] }

// Brace alternatives multiply; past this many expansions the pattern matches only itself.
const MAX_GLOB_ALTERNATIVES = 256

function parseGlob(pattern: string): GlobNode[] | null {
  let i = 0
  const seq = (inBraces: boolean): GlobNode[] | null => {
    const out: GlobNode[] = []
    while (i < pattern.length) {
      const c = pattern[i]
      if (inBraces && (c === ',' || c === '}')) return out
      if (c === '*') out.push({ t: 'star' })
      else if (c === '?') out.push({ t: 'any' })
      else if (c === '[') {
        const end = pattern.indexOf(']', i + 2)
        if (end < 0) return null
        let body = pattern.slice(i + 1, end)
        const negated = body.startsWith('!')
        if (negated) body = body.slice(1)
        out.push(body.length === 3 && body[1] === '-' ? { t: 'class', negated, chars: '', range: [body[0], body[2]] } : { t: 'class', negated, chars: body })
        i = end
      } else if (c === '{') {
        i++
        const options: GlobNode[][] = []
        for (;;) {
          const option = seq(true)
          if (!option || i >= pattern.length) return null
          options.push(option)
          if (pattern[i++] === '}') break
        }
        out.push({ t: 'alt', options })
        continue
      } else out.push({ t: 'lit', c })
      i++
    }
    return inBraces ? null : out
  }
  return seq(false)
}

function expandGlob(nodes: GlobNode[]): GlobToken[][] | null {
  let out: GlobToken[][] = [[]]
  for (const n of nodes) {
    if (n.t !== 'alt') {
      for (const s of out) s.push(n)
      continue
    }
    const next: GlobToken[][] = []
    for (const option of n.options) {
      const tails = expandGlob(option)
      if (!tails) return null
      for (const head of out) for (const tail of tails) next.push([...head, ...tail])
      if (next.length > MAX_GLOB_ALTERNATIVES) return null
    }
    out = next
  }
  return out
}

function tokenMatches(tok: GlobToken, ch: string): boolean {
  if (tok.t === 'any') return true
  if (tok.t === 'lit') return tok.c === ch
  if (tok.t !== 'class') return false
  const inClass = tok.range ? ch >= tok.range[0] && ch <= tok.range[1] : tok.chars.includes(ch)
  return inClass !== tok.negated
}

// Linear in the name per star: on a mismatch it resumes after the last star,
// never revisiting earlier ones, so no pattern can make it backtrack exponentially.
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
 * A namespace pattern as Velero matches it (gobwas/glob, no separators): `*`
 * any run, `?` one character, `{a,b}` alternatives, and a class: `[a-z]` is a
 * range only when it is exactly one (as `[!a-z]`); otherwise every character
 * between the brackets is listed as written, so `[ab-d]` is a, b, - and d.
 * A pattern that does not compile matches only itself. Patterns come from the
 * cluster, so matching never goes through a backtracking regular expression.
 */
export function veleroGlobMatch(pattern: string, name: string): boolean {
  if (!/[*?[{]/.test(pattern)) return pattern === name
  const nodes = parseGlob(pattern)
  const alternatives = nodes && expandGlob(nodes)
  if (!alternatives) return pattern === name
  return alternatives.some((tokens) => matchTokens(tokens, name))
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
