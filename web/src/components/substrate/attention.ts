import type { SubstrateAttention, SubstrateWorkspace } from '../../api/substrateWorkspace'
import { INCOMPLETE_COVERAGE_STATES } from '../workspace/coverage'
import { API_AREAS, attentionArea, coverageInPart, type AttentionArea } from './readings'

export interface SubstrateAttentionCount {
  /** `null` when nothing was found but the read was partial, so zero would claim too much. */
  count: number | null
  lowerBound: boolean
  title: string
}

const AREA_NOUN: Record<AttentionArea, string> = {
  actors: 'actors',
  templates: 'templates',
  workers: 'workers',
  pools: 'pools',
  nodes: 'nodes',
}

/** Whether what an area's rules read was read in full. */
function areaRead(data: SubstrateWorkspace, area: AttentionArea): { read: boolean; why?: string } {
  const connected = data.apiTier.state === 'connected'
  if (area === 'pools' || area === 'nodes') {
    // Pool rows join Kubernetes' WorkerPools with Substrate's workers.
    if (INCOMPLETE_COVERAGE_STATES.has(data.coverage.pools.state)) return { read: false, why: 'some WorkerPools could not be read' }
    if (!connected || coverageInPart(data.coverage?.workers)) return { read: false, why: 'Substrate’s workers were not read' }
    return { read: true }
  }
  if (!connected) return { read: false, why: 'Radar is not connected to Substrate’s API' }
  const cov = data.coverage
  const parts = area === 'actors' ? [cov?.actors] : area === 'templates' ? [cov?.templates, cov?.workers] : [cov?.workers, cov?.actors]
  if (parts.some((c) => coverageInPart(c))) return { read: false, why: `some ${AREA_NOUN[area]} could not be read` }
  return { read: true }
}

function countOf(count: number, read: { read: boolean; why?: string }, what: (n: number) => string): SubstrateAttentionCount {
  if (!read.read && count === 0) {
    return { count: null, lowerBound: true, title: `Unknown: ${read.why}, so nothing found does not mean nothing needs attention.` }
  }
  const note = read.read ? '' : ` ${read.why ? `${read.why[0].toUpperCase()}${read.why.slice(1)}` : 'Some was not read'}, so there may be more.`
  return { count, lowerBound: !read.read, title: `${read.read ? '' : 'At least '}${what(count)}.${note}` }
}

/** Rows that need a look: an info row is expected and still within its budget. */
function needing(data: SubstrateWorkspace): SubstrateAttention[] {
  return data.needsAttention.filter((a) => a.severity !== 'info')
}

/**
 * The integration row's sidebar badge: how many attention items Needs
 * attention lists. Rules that need Substrate's API are not evaluated while it
 * is not read, so then the count is a lower bound and its zero unknown.
 */
export function substrateAttentionCount(data: SubstrateWorkspace): SubstrateAttentionCount {
  const areas: AttentionArea[] = ['actors', 'templates', 'workers', 'pools']
  const unread = areas.map((a) => areaRead(data, a)).find((r) => !r.read)
  return countOf(needing(data).length, unread ?? { read: true }, (n) => `${n} attention ${n === 1 ? 'item' : 'items'}`)
}

const subjectKey = (s: SubstrateAttention['subject']) => `${s.kind}/${s.namespace ?? s.atespace ?? ''}/${s.name ?? s.filter?.template ?? ''}`

/** The actors the Actors badge counts, by why: crashed, and stuck in a transition. */
export function actorAttentionParts(data: SubstrateWorkspace): { crashed: number; stuck: number } {
  const rows = needing(data)
  const sum = (rule: SubstrateAttention['rule']) => rows.filter((a) => a.rule === rule).reduce((n, a) => n + a.count, 0)
  return { crashed: sum('crashedActors'), stuck: sum('stuckTransition') }
}

/**
 * Each screen's badge counts the objects of that screen needing attention, so
 * it matches what the screen shows: actors crashed or stuck in a transition;
 * templates, workers and pools named by a row (each once).
 */
export function substrateScreenCounts(data: SubstrateWorkspace): Record<AttentionArea, SubstrateAttentionCount> {
  const rows = needing(data)
  const { crashed, stuck } = actorAttentionParts(data)
  const distinct = (area: AttentionArea) => new Set(rows.filter((a) => attentionArea(a) === area).map((a) => subjectKey(a.subject))).size
  const objects = (one: string, many: string) => (n: number) => `${n} ${n === 1 ? `${one} needs` : `${many} need`} attention`
  return {
    actors: countOf(crashed + stuck, areaRead(data, 'actors'), (n) => `${n} ${n === 1 ? 'actor needs' : 'actors need'} attention: ${crashed} crashed, ${stuck} stuck in a transition`),
    templates: countOf(distinct('templates'), areaRead(data, 'templates'), objects('template', 'templates')),
    workers: countOf(distinct('workers'), areaRead(data, 'workers'), objects('worker', 'workers')),
    pools: countOf(distinct('pools'), areaRead(data, 'pools'), objects('pool', 'pools')),
    nodes: countOf(distinct('nodes'), areaRead(data, 'nodes'), objects('node', 'nodes')),
  }
}

/** Whether the Needs attention list left out rules because their inputs were not read. */
export function attentionInPart(data: SubstrateWorkspace): boolean {
  return [...API_AREAS, 'pools' as const].some((a) => !areaRead(data, a).read)
}
