import type { CNPGBackend } from '../../api/cnpg-sessions'

export interface BlockingNode {
  session: CNPGBackend
  children: BlockingNode[]
  /** Blockers of this session other than the parent it is shown under. */
  alsoWaitsOn: number[]
  /** This session waits on one of its own ancestors: a lock cycle (deadlock). */
  cycle?: boolean
}

/**
 * Blocker → victim trees from pg_blocking_pids. A root blocks others and
 * waits on nobody listed; a victim appears under each blocker it waits on.
 * Sessions left over after walking from the roots are in a cycle (a deadlock
 * PostgreSQL has not resolved yet) and become roots of their own, marked.
 */
export function buildBlockingTree(sessions: CNPGBackend[]): BlockingNode[] {
  const byPid = new Map(sessions.map((s) => [s.pid, s]))
  const victimsOf = new Map<number, CNPGBackend[]>()
  for (const s of sessions) {
    for (const b of s.blockedBy) {
      if (!byPid.has(b)) continue
      victimsOf.set(b, [...(victimsOf.get(b) ?? []), s])
    }
  }
  const seen = new Set<number>()
  const build = (s: CNPGBackend, parent: number | undefined, path: Set<number>): BlockingNode => {
    seen.add(s.pid)
    const node: BlockingNode = { session: s, children: [], alsoWaitsOn: s.blockedBy.filter((b) => b !== parent) }
    const nextPath = new Set(path).add(s.pid)
    for (const v of victimsOf.get(s.pid) ?? []) {
      if (nextPath.has(v.pid)) {
        node.cycle = true
        continue
      }
      node.children.push(build(v, s.pid, nextPath))
    }
    return node
  }
  const roots = sessions
    .filter((s) => s.blockedBy.every((b) => !byPid.has(b)) && (victimsOf.get(s.pid)?.length ?? 0) > 0)
    .map((s) => build(s, undefined, new Set()))
  // Waiting on a backend that is not listed (capped, or ended between reads).
  for (const s of sessions) {
    if (!seen.has(s.pid) && s.blockedBy.length > 0 && s.blockedBy.every((b) => !byPid.has(b))) roots.push(build(s, undefined, new Set()))
  }
  for (const s of sessions) {
    if (!seen.has(s.pid)) {
      const node = build(s, undefined, new Set())
      node.cycle = true
      roots.push(node)
    }
  }
  return roots
}

/** How many sessions wait behind this one, directly or transitively. */
export function countVictims(node: BlockingNode): number {
  const pids = new Set<number>()
  const walk = (n: BlockingNode) => {
    for (const c of n.children) {
      pids.add(c.session.pid)
      walk(c)
    }
  }
  walk(node)
  return pids.size
}

export interface CNPGConnectionFigure {
  /** e.g. "6 of 97 usable" */
  value: string
  /** What the figure is measured against, and from where. */
  detail: string
  /** used / usable, for a headroom bar; undefined when the limit is unknown. */
  ratio?: number
  tone?: 'degraded' | 'unhealthy'
}

function connectionTone(ratio: number | undefined): CNPGConnectionFigure['tone'] {
  if (ratio === undefined) return undefined
  return ratio >= 0.95 ? 'unhealthy' : ratio >= 0.8 ? 'degraded' : undefined
}

/**
 * The one connections figure the Sessions section shows. pg_stat_activity
 * (read over exec) knows the superuser reserve, so it gives usable headroom;
 * the exporter's count stands in when that read is unavailable and says what
 * it cannot account for.
 */
export function cnpgConnectionFigure(
  exec: { maxConnections?: number; superuserReservedConnections?: number; clientBackends?: number } | undefined,
  exporter?: { sessionsTotal?: number; maxConnections?: number },
): CNPGConnectionFigure | undefined {
  if (exec?.maxConnections !== undefined && exec.clientBackends !== undefined) {
    const reserved = exec.superuserReservedConnections ?? 0
    const usable = exec.maxConnections - reserved
    const ratio = usable > 0 ? exec.clientBackends / usable : 1
    return {
      value: `${exec.clientBackends} of ${usable} usable`,
      detail: `max_connections ${exec.maxConnections}, ${reserved} reserved for superusers · client backends from pg_stat_activity`,
      ratio,
      tone: connectionTone(ratio),
    }
  }
  if (exporter?.sessionsTotal === undefined) return undefined
  if (exporter.maxConnections === undefined) {
    return { value: `${exporter.sessionsTotal}`, detail: 'max_connections not reported · sessions from the metrics exporter' }
  }
  const ratio = exporter.maxConnections > 0 ? exporter.sessionsTotal / exporter.maxConnections : 1
  return {
    value: `${exporter.sessionsTotal} of ${exporter.maxConnections}`,
    detail: `max_connections ${exporter.maxConnections}; the superuser reserve is not read without exec · sessions from the metrics exporter`,
    ratio,
    tone: connectionTone(ratio),
  }
}

/**
 * Whether the metrics API itself is missing, from each instance's metrics
 * read (null = none for that Pod, undefined = not answered yet). Only every
 * instance answering null says so; one null may be a Pod not scraped yet.
 */
export function cnpgMetricsApiMissing(results: (unknown | null | undefined)[]): boolean {
  return results.length > 0 && results.every((r) => r === null)
}
