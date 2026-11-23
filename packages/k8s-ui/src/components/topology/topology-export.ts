import { Box, Tag } from 'lucide-react'
import { displayKind, type HealthStatus } from '../../types'
import type { GraphBoxSpec, GraphGroupTone, GraphInnerBoxSpec, GraphNodeMapper } from '../../utils/graph-model'
import { pluralize } from '../../utils/pluralize'
import { getSubtitle } from './K8sResourceNode'
import { GRID_CARD_H, GRID_CARD_W, GRID_GAP, GRID_PAD } from './layout'

const STATUSES: readonly HealthStatus[] = ['healthy', 'degraded', 'unhealthy', 'neutral', 'unknown']

function isStatus(value: unknown): value is HealthStatus {
  return typeof value === 'string' && STATUSES.some(s => s === value)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function groupTone(value: unknown): GraphGroupTone {
  return value === 'app' || value === 'label' ? value : 'namespace'
}

// Kinds a collapsed chip lists by its size tier (0-9, 10-99, 100-999, 1000+ resources), as GroupNode does.
function kindsShown(nodeCount: number): number {
  const tier = nodeCount <= 0 ? 0 : Math.min(3, Math.floor(Math.log10(nodeCount)))
  return [2, 5, 8, 12][tier]
}

function kindSummary(kindCounts: unknown, max: number): string | undefined {
  if (!isRecord(kindCounts)) return undefined
  const entries = Object.entries(kindCounts)
    .filter((e): e is [string, number] => typeof e[1] === 'number')
    .sort(([, a], [, b]) => b - a)
  if (entries.length === 0) return undefined
  const shown = entries.slice(0, max).map(([kind, count]) => pluralize(count, kind))
  const rest = entries.length - max
  return rest > 0 ? `${shown.join(', ')} +${rest} more` : shown.join(', ')
}

// Workload cards of a card-grid group, placed as GroupNode lays them out.
function workloadCards(data: Record<string, unknown>): GraphInnerBoxSpec[] {
  if (!Array.isArray(data.workloadCards)) return []
  const cards = data.workloadCards.filter(isRecord)
  const columns = typeof data.gridColumns === 'number' && data.gridColumns > 0 ? data.gridColumns : Math.min(4, cards.length || 1)
  return cards.map((card, i) => {
    const kind = str(card.kind)
    const detail = str(card.subtitle)
    const resources = typeof card.resourceCount === 'number' ? card.resourceCount : undefined
    return {
      shape: 'resource',
      kind,
      name: str(card.name),
      kindLabel: displayKind(kind),
      ...(detail ? { detail } : {}),
      ...(isStatus(card.status) ? { status: card.status } : {}),
      ...(resources !== undefined ? { count: resources } : {}),
      x: GRID_PAD.left + (i % columns) * (GRID_CARD_W + GRID_GAP),
      y: GRID_PAD.top + Math.floor(i / columns) * (GRID_CARD_H + GRID_GAP),
      w: GRID_CARD_W,
      h: GRID_CARD_H,
    }
  })
}

function describeGroup(data: Record<string, unknown>): GraphBoxSpec {
  const tone = groupTone(data.type)
  const nodeCount = typeof data.nodeCount === 'number' ? data.nodeCount : undefined
  const cards = data.displayLevel === 'cardGrid' ? workloadCards(data) : undefined
  // A card grid's badge counts its workload cards, as on screen; other groups count resources.
  const count = cards ? cards.length : nodeCount
  const base = {
    kind: 'Namespace',
    name: str(data.name),
    tone,
    count,
    icon: tone === 'namespace' ? Box : Tag,
  }
  if (data.displayLevel === 'topology') return { ...base, shape: 'group' }
  const worst = data.worstStatus
  const hasProblems = typeof data.unhealthyCount === 'number' && data.unhealthyCount > 0
  return {
    ...base,
    ...(hasProblems && isStatus(worst) ? { status: worst } : {}),
    shape: 'collapsed-group',
    ...(cards ? { inner: cards } : { detail: kindSummary(data.kindCounts, kindsShown(nodeCount ?? 0)) }),
  }
}

/** Topology nodes as export boxes: resources, Internet, and namespace / app / label groups. */
export const describeTopologyNode: GraphNodeMapper = ({ type, data }) => {
  if (type === 'group') return describeGroup(data)
  if (type !== 'k8sResource') return null
  const kind = str(data.kind)
  const name = str(data.name)
  if (kind === 'Internet') return { shape: 'internet', kind, name: 'Internet' }
  const nodeData = isRecord(data.nodeData) ? data.nodeData : {}
  const detail = getSubtitle(kind, nodeData)
  return {
    shape: 'resource',
    kind,
    name,
    kindLabel: kind === 'PodGroup' ? 'Pod Group' : displayKind(kind),
    ...(detail ? { detail } : {}),
    ...(isStatus(data.status) ? { status: data.status } : {}),
  }
}
