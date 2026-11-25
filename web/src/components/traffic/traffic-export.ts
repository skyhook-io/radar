import { CircleQuestionMark, Database, Globe, Puzzle, Server } from 'lucide-react'
import { getTopologyIcon } from '@skyhook-io/k8s-ui'
import type { GraphBoxRow, GraphNodeMapper } from '@skyhook-io/k8s-ui/utils/graph-model'
import { formatVolume, isExternalKind, MAX_VISIBLE_PORTS } from './trafficFilters'

// Hex values of the Tailwind 500 shades the traffic nodes use for these categories.
const CATEGORY_COLORS: Record<string, string> = {
  database: '#8B5CF6',
  cloud: '#06B6D4',
  monitoring: '#84CC16',
  payment: '#10B981',
  auth: '#F59E0B',
  email: '#EC4899',
  messaging: '#A855F7',
  cache: '#F97316',
  infra: '#64748B',
  web: '#3B82F6',
}
const HOT = '#F97316'
const EXTERNAL = '#EAB308'
const ADDON = '#A855F7'
const HEALTHY = '#22C55E'
const ADDON_FILL = '#581C87'

function str(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
}

function num(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

// The card's port rows (":80   230"), or its connection total when it lists no port.
function trafficRows(data: Record<string, unknown>, hot: boolean): GraphBoxRow[] {
  const rateBased = data.connLabel === 'req/s'
  const ports = (Array.isArray(data.ports) ? data.ports : []).flatMap((p: unknown) => {
    if (typeof p !== 'object' || p === null || !('port' in p)) return []
    const port = num(p.port)
    const connections = ('connections' in p ? num(p.connections) : undefined) ?? 0
    return port ? [{ port, connections }] : []
  })
  if (ports.length > 0) {
    const rows: GraphBoxRow[] = ports.slice(0, MAX_VISIBLE_PORTS).map(({ port, connections }) => ({
      text: `:${port}`,
      code: true,
      hot,
      // No figure for a port with no measured traffic, as on the card.
      ...(connections > 0 ? { value: `${formatVolume(connections, rateBased)}${rateBased ? '/s' : ''}` } : {}),
    }))
    if (ports.length > MAX_VISIBLE_PORTS) rows.push({ text: `+${ports.length - MAX_VISIBLE_PORTS} more`, muted: true })
    return rows
  }
  const total = num(data.totalConnections)
  return total && total > 0 ? [{ text: `${formatVolume(total, rateBased)} ${str(data.connLabel) ?? 'conn'}`, hot }] : []
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/** Live Traffic nodes as export boxes. Namespaces are tints on the nodes; only the addon group is a container. */
export const describeTrafficNode: GraphNodeMapper = ({ type, data }) => {
  if (type === 'addonGroup') {
    return { shape: 'group', tone: 'addon', kind: 'Addon', name: 'Cluster Addons', icon: Puzzle }
  }
  if (type !== 'traffic') return null
  const kind = str(data.kind) ?? 'Unknown'
  const name = str(data.label) ?? ''
  if (kind === 'Internet' || kind === 'AddonInternet') {
    return { shape: 'internet', kind, name: 'Internet', accent: kind === 'AddonInternet' ? ADDON : '#0EA5E9', icon: Globe }
  }
  const namespace = str(data.namespace)
  const category = str(data.serviceCategory)
  const isAddonNode = data.isAddonNode === true
  const external = isExternalKind(kind)
  const workloadKind = str(data.workloadKind)
  const shownKind = kind === 'Workload' && workloadKind ? workloadKind : kind
  const namespaceColor = str(data.namespaceColor)
  const fill = isAddonNode ? ADDON_FILL : !external && kind !== 'Addon' ? namespaceColor : undefined
  const accent = data.isHotPath === true
    ? HOT
    : kind === 'Addon' || isAddonNode
      ? ADDON
      : external
        ? (category && CATEGORY_COLORS[category]) || EXTERNAL
        : HEALTHY
  const icon = kind === 'Addon'
    ? Puzzle
    : kind.toLowerCase() === 'unknown'
      ? CircleQuestionMark
      : external
        ? category === 'database' ? Database : Server
        : getTopologyIcon(shownKind)
  const detail = namespace ?? (category ? capitalize(category) : kind === 'Addon' ? 'Monitoring, logging, etc.' : undefined)
  const workload = str(data.workload)
  // The card lists the workload under the namespace when it differs from the endpoint name.
  const rows = [...(workload && workload !== name ? [{ text: workload }] : []), ...trafficRows(data, data.isHotPath === true)]
  // External cards are washed in their category color (yellow without one), as on screen.
  const tint = external ? (category && CATEGORY_COLORS[category] ? { color: CATEGORY_COLORS[category], opacity: 0.2 } : { color: EXTERNAL, opacity: 0.1 }) : undefined
  return {
    shape: 'resource',
    kind: shownKind,
    name,
    kindLabel: shownKind,
    accent,
    icon,
    ...(detail ? { detail } : {}),
    ...(category ? { category } : {}),
    ...(fill ? { fill, textColor: '#FFFFFF' } : {}),
    ...(tint ? { tint } : {}),
    nameFirst: true,
    ...(rows.length > 0 ? { rows } : {}),
  }
}
