import { Box, Tag } from 'lucide-react'
import { displayKind, type HealthStatus } from '../../types'
import type { GraphBoxSpec, GraphGroupTone, GraphInnerBoxSpec, GraphNodeMapper } from '../../utils/graph-model'
import { pluralize } from '../../utils/pluralize'
import { getSubtitle } from './K8sResourceNode'
import { GRID_CARD_H, GRID_CARD_W, GRID_GAP, GRID_PAD } from './layout'

const STATUSES: readonly HealthStatus[] = ['healthy', 'degraded', 'unhealthy', 'neutral', 'unknown']
const MAX_KINDS_IN_CHIP = 3

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

function kindSummary(kindCounts: unknown): string | undefined {
  if (!isRecord(kindCounts)) return undefined
  const entries = Object.entries(kindCounts)
    .filter((e): e is [string, number] => typeof e[1] === 'number')
    .sort(([, a], [, b]) => b - a)
  if (entries.length === 0) return undefined
  const shown = entries.slice(0, MAX_KINDS_IN_CHIP).map(([kind, count]) => pluralize(count, kind))
  const rest = entries.length - MAX_KINDS_IN_CHIP
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
    return {
      shape: 'resource',
      kind,
      name: str(card.name),
      kindLabel: displayKind(kind),
      ...(detail ? { detail } : {}),
      ...(isStatus(card.status) ? { status: card.status } : {}),
      x: GRID_PAD.left + (i % columns) * (GRID_CARD_W + GRID_GAP),
      y: GRID_PAD.top + Math.floor(i / columns) * (GRID_CARD_H + GRID_GAP),
      w: GRID_CARD_W,
      h: GRID_CARD_H,
    }
  })
}

function describeGroup(data: Record<string, unknown>): GraphBoxSpec {
  const tone = groupTone(data.type)
  const count = typeof data.nodeCount === 'number' ? data.nodeCount : undefined
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
    ...(data.displayLevel === 'cardGrid' ? { inner: workloadCards(data) } : { detail: kindSummary(data.kindCounts) }),
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
