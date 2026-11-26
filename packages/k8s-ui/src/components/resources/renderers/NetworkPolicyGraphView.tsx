import { useState, type CSSProperties } from 'react'
import { clsx } from 'clsx'
import { ArrowDown } from 'lucide-react'
import { Collapse, useDisclosure } from '../../ui/Collapse'
import { StatusDot } from '../../ui/status-tone'
import { NetworkPolicyGroupList } from './NetworkPolicyGroupList'
import { PortChips, StagedNote } from './NetworkPolicyRulesView'
import { NETWORK_POLICY_PEER_DOTS } from './network-policy-peer-styles'
import type { NetworkPolicySummary, PolicyDirectionSummary, PolicyGroup } from '../../../utils/network-policy-summary'

type Direction = 'ingress' | 'egress'

const NODE_HEIGHT = 60
const NODE_GAP = 8
// Wide enough for the longest single port label, a range such as TCP/30000-32767.
const EDGE_WIDTH = 92
// Sources carry the longest titles; the target and the destinations need less room.
const GRID_COLUMNS = `minmax(0,3fr) ${EDGE_WIDTH}px minmax(140px,1.6fr) ${EDGE_WIDTH}px minmax(0,2fr)`
const FLOW: Record<Direction, string> = { ingress: 'inbound', egress: 'outbound' }
const EDGE_COLOR: Record<Direction, string> = { ingress: 'text-blue-500/70', egress: 'text-purple-500/70' }

type GraphNode =
  | { key: string; fp: string; type: 'group'; rule: number; firstOfRule: boolean; ports: string[]; group: PolicyGroup }
  | { key: string; type: 'deny' }
  | { key: string; type: 'open' }

function directionNodes(dir: Direction, summary: PolicyDirectionSummary): GraphNode[] {
  if (!summary.restricted) return [{ key: `${dir}-open`, type: 'open' }]
  if (summary.denyAll) return [{ key: `${dir}-deny`, type: 'deny' }]
  return summary.rules.flatMap((rule) =>
    rule.groups.map((group, i) => ({
      key: `${dir}-${rule.index}-${i}`,
      // Selection follows content, not position: a live update can renumber the rules.
      fp: [dir, group.kind, group.title, rule.ports.join(','), ...group.raw].join('\n'),
      type: 'group' as const,
      rule: rule.index,
      firstOfRule: i === 0,
      ports: rule.ports,
      group,
    })),
  )
}

export function NetworkPolicyGraphView({ summary, staged = false }: { summary: NetworkPolicySummary; staged?: boolean }) {
  const ingress = directionNodes('ingress', summary.ingress)
  const egress = directionNodes('egress', summary.egress)
  const [selectedFp, setSelectedFp] = useState<string | null>(null)
  // Kept after close so the panel still has content during its exit transition.
  const [shownFp, setShownFp] = useState<string | null>(null)
  const groups = [...ingress, ...egress].flatMap((n) => (n.type === 'group' ? [n] : []))
  const shown = groups.find((n) => n.fp === shownFp)
  // A live update removed the selected group: close, and stay closed if it comes back.
  if (selectedFp !== null && !groups.some((n) => n.fp === selectedFp)) setSelectedFp(null)
  const open = selectedFp !== null && shown?.fp === selectedFp
  const { panelId } = useDisclosure(open)

  const toggle = (fp: string) => {
    if (selectedFp === fp) {
      setSelectedFp(null)
    } else {
      setSelectedFp(fp)
      setShownFp(fp)
    }
  }
  const rows = Math.max(ingress.length, egress.length, 2)
  const height = rows * NODE_HEIGHT + (rows - 1) * NODE_GAP

  return (
    <div className="@container space-y-3">
      {staged && <StagedNote />}
      <div
        data-graph-columns
        className="flex flex-col gap-2 @2xl:grid @2xl:items-center @2xl:gap-0"
        style={{ gridTemplateColumns: GRID_COLUMNS, '--node-h': `${NODE_HEIGHT}px` } as CSSProperties}
      >
        <NodeColumn dir="ingress" nodes={ingress} selectedFp={selectedFp} onToggle={toggle} panelId={panelId} />
        <Edges dir="ingress" nodes={ingress} height={height} staged={staged} />
        <DownArrow />
        <div data-graph-target className="rounded-lg border border-indigo-500/40 bg-theme-elevated px-3 py-2.5 text-center min-w-0">
          <div className="text-[10px] uppercase tracking-wider text-theme-text-tertiary">Selected pods</div>
          <div className="mt-0.5 text-xs font-medium text-theme-text-primary break-words">{summary.target}</div>
          {(['ingress', 'egress'] as const)
            .filter((dir) => summary[dir].restricted && !summary[dir].denyAll)
            .map((dir) => (
              <div key={dir} className="mt-1.5 text-[10.5px] text-red-600 dark:text-red-400">{`other ${FLOW[dir]}: denied`}</div>
            ))}
        </div>
        <DownArrow />
        <Edges dir="egress" nodes={egress} height={height} staged={staged} />
        <NodeColumn dir="egress" nodes={egress} selectedFp={selectedFp} onToggle={toggle} panelId={panelId} />
      </div>
      <Collapse open={open} id={panelId}>
        <div data-graph-detail className="rounded-md border border-theme-border bg-theme-elevated/50 px-2.5 py-2 space-y-2">
          {shown && (
            <>
              <div className="flex items-start justify-between gap-2">
                <span className="text-[10px] uppercase tracking-wider text-theme-text-tertiary">{`Rule ${shown.rule}`}</span>
                <PortChips ports={shown.ports} />
              </div>
              <NetworkPolicyGroupList key={shown.key} group={shown.group} />
            </>
          )}
        </div>
      </Collapse>
    </div>
  )
}

function NodeColumn({
  dir,
  nodes,
  selectedFp,
  onToggle,
  panelId,
}: {
  dir: Direction
  nodes: GraphNode[]
  selectedFp: string | null
  onToggle: (fp: string) => void
  panelId: string
}) {
  return (
    <div className="flex flex-col min-w-0" style={{ gap: NODE_GAP }}>
      {nodes.map((node) => {
        if (node.type === 'open') {
          return (
            <div key={node.key} data-graph-node={dir} className="@2xl:h-[var(--node-h)] rounded-md border border-dashed border-theme-border px-2.5 py-1.5 flex flex-col justify-center">
              <div className="text-xs font-medium text-theme-text-secondary">{dir === 'ingress' ? 'Ingress' : 'Egress'}</div>
              <div className="text-[10.5px] text-theme-text-tertiary">not restricted by this policy</div>
            </div>
          )
        }
        if (node.type === 'deny') {
          return (
            <div key={node.key} data-graph-node={dir} className="@2xl:h-[var(--node-h)] rounded-md border border-red-500/30 bg-theme-elevated px-2.5 py-1.5 flex items-center gap-1.5 text-xs text-theme-text-secondary">
              <StatusDot tone="unhealthy" className="shrink-0" />
              {`All ${FLOW[dir]} denied`}
            </div>
          )
        }
        const selected = selectedFp === node.fp
        const samples = node.group.items.slice(0, 2)
        return (
          <button
            key={node.key}
            type="button"
            data-graph-node={dir}
            aria-pressed={selected}
            aria-controls={panelId}
            onClick={() => onToggle(node.fp)}
            className={clsx(
              '@2xl:h-[var(--node-h)] min-w-0 overflow-hidden rounded-md border px-2.5 py-1.5 text-left transition-colors hover:bg-theme-hover',
              selected ? 'border-theme-text-tertiary bg-theme-hover' : 'border-theme-border bg-theme-elevated/50',
            )}
          >
            <div className="flex items-center gap-1.5 min-w-0">
              <span className={clsx('w-1.5 h-1.5 rounded-full shrink-0', NETWORK_POLICY_PEER_DOTS[node.group.peerType])} />
              <span className="text-xs font-medium text-theme-text-primary truncate" title={node.group.title}>{node.group.title}</span>
              <span data-rule-tag className="ml-auto shrink-0 text-[9.5px] uppercase tracking-wider text-theme-text-tertiary">{`Rule ${node.rule}`}</span>
            </div>
            {samples.length > 0 && (
              <div className="mt-0.5 truncate text-[10.5px] font-mono text-theme-text-tertiary">
                {samples.map((s, i) => (
                  <span key={i} data-sample>{`${i > 0 ? ', ' : ''}${s.label}`}</span>
                ))}
                {node.group.items.length > 2 && <span>{` +${node.group.items.length - 2}`}</span>}
              </div>
            )}
            {/* Wide mode draws the ports on the edge, which is aria-hidden; keep them readable here. */}
            <div data-node-ports className="mt-1 @2xl:sr-only">
              <PortChips ports={node.ports} />
            </div>
          </button>
        )
      })}
    </div>
  )
}

function Edges({ dir, nodes, height, staged }: { dir: Direction; nodes: GraphNode[]; height: number; staged: boolean }) {
  const columnHeight = nodes.length * NODE_HEIGHT + (nodes.length - 1) * NODE_GAP
  const top = (height - columnHeight) / 2
  const mid = height / 2
  return (
    <svg
      aria-hidden="true"
      width={EDGE_WIDTH}
      height={height}
      viewBox={`0 0 ${EDGE_WIDTH} ${height}`}
      className={clsx('hidden @2xl:block overflow-visible', EDGE_COLOR[dir])}
    >
      {nodes.map((node, i) => {
        const y = top + i * (NODE_HEIGHT + NODE_GAP) + NODE_HEIGHT / 2
        // Ingress flows from the node into the target; egress from the target out to the node.
        const [y1, y2] = dir === 'ingress' ? [y, mid] : [mid, y]
        const dashed = staged || node.type === 'open'
        // One label per rule: its groups share the ports. The node lists them all.
        const label =
          node.type === 'group' && node.firstOfRule && node.ports.length > 0
            ? node.ports.length === 1 ? node.ports[0] : `${node.ports.length} ports`
            : null
        return (
          <g key={node.key}>
            <path
              data-edge={dir}
              d={`M0,${y1} C${EDGE_WIDTH / 2},${y1} ${EDGE_WIDTH / 2},${y2} ${EDGE_WIDTH - 4},${y2}`}
              fill="none"
              stroke="currentColor"
              strokeWidth={1.5}
              strokeDasharray={dashed ? '4 3' : undefined}
            />
            <path d={`M${EDGE_WIDTH - 8},${y2 - 3} L${EDGE_WIDTH - 3},${y2} L${EDGE_WIDTH - 8},${y2 + 3}`} fill="none" stroke="currentColor" strokeWidth={1.5} />
            {label && (
              <text data-edge-label x={dir === 'ingress' ? 4 : EDGE_WIDTH - 4} y={y - 5} textAnchor={dir === 'ingress' ? 'start' : 'end'} className="fill-theme-text-secondary font-mono" fontSize={9}>
                {label}
              </text>
            )}
          </g>
        )
      })}
    </svg>
  )
}

function DownArrow() {
  return (
    <div className="flex justify-center text-theme-text-tertiary @2xl:hidden" aria-hidden="true">
      <ArrowDown className="w-3.5 h-3.5" />
    </div>
  )
}
