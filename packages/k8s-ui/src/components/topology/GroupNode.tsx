import { memo } from 'react'
import { NodeProps, Handle, Position } from '@xyflow/react'
import { Box, LayoutGrid, Maximize2, Minus, Tag, Workflow } from 'lucide-react'
import { healthToSeverity, SEVERITY_DOT } from '../../utils/badge-colors'
import type { HealthStatus } from '../../types'
import { Tooltip } from '../ui/Tooltip'
import type { WorkloadCard, GroupDisplayLevel } from './layout'
import { pluralize } from '../../utils/pluralize'
import { getTopologyIcon } from '../../utils/resource-icons'

interface GroupNodeData {
  type: 'namespace' | 'app' | 'label'
  name: string
  label?: string
  nodeCount: number
  collapsed: boolean
  displayLevel: GroupDisplayLevel
  onSetLevel: (groupId: string, level: GroupDisplayLevel) => void
  onCardClick?: (nodeId: string) => void
  onMaximizeNamespace?: (namespace: string) => void
  hideHeader?: boolean
  worstStatus?: HealthStatus
  unhealthyCount?: number
  kindCounts?: Record<string, number>
  workloadCards?: WorkloadCard[]
  gridColumns?: number
}

export const GroupNode = memo(function GroupNode({
  id,
  data,
  width,
  height,
}: NodeProps & { data: GroupNodeData }) {
  const {
    type, name, label, nodeCount, displayLevel,
    onSetLevel, onCardClick, onMaximizeNamespace, hideHeader,
    worstStatus, unhealthyCount, kindCounts,
    workloadCards, gridColumns,
  } = data
  const hasProblems = (unhealthyCount ?? 0) > 0
  const statusDotColor = hasProblems && worstStatus ? SEVERITY_DOT[healthToSeverity(worstStatus)] : null

  const getIcon = () => {
    switch (type) {
      case 'namespace':
        return Box
      case 'app':
      case 'label':
        return Tag
      default:
        return Box
    }
  }

  const getBorderStyle = (): React.CSSProperties => {
    switch (type) {
      case 'namespace':
        return { border: '2px solid var(--group-border-namespace)' }
      case 'app':
        return { border: '2px solid var(--group-border-app)' }
      case 'label':
        return { border: '2px solid var(--group-border-label)' }
      default:
        return { border: '2px solid var(--border-default)' }
    }
  }

  const getHeaderBgStyle = (): React.CSSProperties => {
    switch (type) {
      case 'namespace':
        return { backgroundColor: 'var(--group-header-namespace)' }
      case 'app':
        return { backgroundColor: 'var(--group-header-app)' }
      case 'label':
        return { backgroundColor: 'var(--group-header-label)' }
      default:
        return { backgroundColor: 'var(--bg-hover)' }
    }
  }

  const getLabelStyle = (): React.CSSProperties => {
    switch (type) {
      case 'namespace':
        return { color: 'var(--group-label-namespace)' }
      case 'app':
        return { color: 'var(--group-label-app)' }
      case 'label':
        return { color: 'var(--group-label-label)' }
      default:
        return { color: 'var(--text-secondary)' }
    }
  }

  const getIconStyle = (): React.CSSProperties => {
    switch (type) {
      case 'namespace':
        return { color: 'var(--group-icon-namespace)' }
      case 'app':
        return { color: 'var(--group-icon-app)' }
      case 'label':
        return { color: 'var(--group-icon-label)' }
      default:
        return { color: 'var(--text-secondary)' }
    }
  }

  const Icon = getIcon()

  // Invisible handles (shared across all levels)
  const handles = (
    <>
      <Handle type="target" position={Position.Left} className="!bg-transparent !border-0 !w-0 !h-0" />
      <Handle type="source" position={Position.Right} className="!bg-transparent !border-0 !w-0 !h-0" />
    </>
  )

  // Level control buttons — shared across all headers
  const TIP_DELAY = 100
  const levelControls = (
    <div className="flex items-center gap-0.5 shrink-0" onClick={(e) => e.stopPropagation()}>
      <Tooltip content="Collapse" delay={TIP_DELAY} position="bottom">
        <button
          className={`p-1 rounded transition-colors ${displayLevel === 'chip' ? 'opacity-40' : 'hover:bg-white/10'}`}
          onClick={() => displayLevel !== 'chip' && onSetLevel(id, 'chip')}
          disabled={displayLevel === 'chip'}
        >
          <Minus className="w-4 h-4" style={getIconStyle()} />
        </button>
      </Tooltip>
      <Tooltip content="Workload cards" delay={TIP_DELAY} position="bottom">
        <button
          className={`p-1 rounded transition-colors ${displayLevel === 'cardGrid' ? 'opacity-40' : 'hover:bg-white/10'}`}
          onClick={() => displayLevel !== 'cardGrid' && onSetLevel(id, 'cardGrid')}
          disabled={displayLevel === 'cardGrid'}
        >
          <LayoutGrid className="w-4 h-4" style={getIconStyle()} />
        </button>
      </Tooltip>
      <Tooltip content="Full topology" delay={TIP_DELAY} position="bottom">
        <button
          className={`p-1 rounded transition-colors ${displayLevel === 'topology' ? 'opacity-40' : 'hover:bg-white/10'}`}
          onClick={() => displayLevel !== 'topology' && onSetLevel(id, 'topology')}
          disabled={displayLevel === 'topology'}
        >
          <Workflow className="w-4 h-4" style={getIconStyle()} />
        </button>
      </Tooltip>
      {onMaximizeNamespace && type === 'namespace' && (
        <Tooltip content="Focus namespace" delay={TIP_DELAY} position="bottom">
          <button
            className="p-1 rounded hover:bg-white/10 transition-colors"
            onClick={() => onMaximizeNamespace(name)}
          >
            <Maximize2 className="w-4 h-4" style={getIconStyle()} />
          </button>
        </Tooltip>
      )}
    </div>
  )

  // ── Level 1: Chip (compact collapsed card) ──
  if (displayLevel === 'chip') {
    // Size tier: 0-9 → 0, 10-99 → 1, 100-999 → 2, 1000+ → 3
    const tier = nodeCount === 0 ? 0 : Math.min(3, Math.floor(Math.log10(nodeCount)))
    const nameSize = ['text-sm', 'text-xl', 'text-4xl', 'text-6xl'][tier]
    const iconSize = ['w-3.5 h-3.5', 'w-5 h-5', 'w-9 h-9', 'w-12 h-12'][tier]
    const maxPills = [2, 5, 8, 12][tier]
    const chipPadding = ['8px 10px', '10px 12px', '14px 16px', '16px 20px'][tier]

    const kindPills = kindCounts
      ? Object.entries(kindCounts)
          .sort(([, a], [, b]) => b - a)
          .slice(0, maxPills)
      : []

    return (
      <>
        {handles}
        <div
          className="rounded-xl group-header-scaled overflow-hidden"
          style={{ ...getBorderStyle(), ...getHeaderBgStyle(), padding: chipPadding, width: width || '100%', height: height || '100%' }}
        >
          {/* Header row: icon + name + count + status + controls */}
          <div className="flex items-center gap-2 min-w-0">
            <Icon className={`${iconSize} shrink-0`} style={getIconStyle()} />
            <span className={`${nameSize} font-bold truncate`} style={getLabelStyle()}>{name}</span>
            <span className="text-[10px] text-theme-text-secondary shrink-0 bg-theme-surface/50 rounded px-1.5 py-0.5">{nodeCount}</span>
            {statusDotColor && (
              <span className={`w-2 h-2 rounded-full shrink-0 ${statusDotColor}`} />
            )}
            <div className="ml-auto shrink-0">{levelControls}</div>
          </div>
          {/* Kind pills row: colored icons with counts */}
          {kindPills.length > 0 && (
            <div className="flex flex-wrap gap-1 mt-1.5">
              {kindPills.map(([kind, count]) => (
                <div key={kind} className="flex items-center gap-1 bg-theme-surface/50 rounded px-1.5 py-0.5">
                  {(() => {
                    const KindIcon = getTopologyIcon(kind)
                    return <KindIcon className="h-2.5 w-2.5 shrink-0 text-theme-text-tertiary" aria-hidden />
                  })()}
                  <span className="text-[10px] text-theme-text-secondary">{pluralize(count, kind)}</span>
                </div>
              ))}
              {kindCounts && Object.keys(kindCounts).length > maxPills && (
                <span className="text-[10px] text-theme-text-tertiary self-center">+{Object.keys(kindCounts).length - maxPills}</span>
              )}
            </div>
          )}
        </div>
      </>
    )
  }

  // ── Level 2: Card Grid (workload cards inside namespace) ──
  if (displayLevel === 'cardGrid' && workloadCards) {
    const cols = gridColumns || Math.min(4, workloadCards.length || 1)
    return (
      <>
        {handles}
        <div
          className="rounded-xl overflow-hidden group-header-scaled"
          style={{ ...getBorderStyle(), width: width || '100%', height: height || '100%' }}
        >
          {/* Header bar with level controls */}
          <div
            className="w-full relative flex items-center"
            style={{ ...getHeaderBgStyle(), padding: '8px 12px' }}
          >
            <div className="flex items-center gap-2 min-w-0 flex-1">
              <Icon className="shrink-0 w-5 h-5" style={getIconStyle()} />
              <span className="font-bold truncate text-base" style={getLabelStyle()}>{name}</span>
              <span className="shrink-0 text-xs text-theme-text-secondary bg-theme-surface/50 rounded px-1.5 py-0.5">
                {workloadCards.length}
              </span>
            </div>
            <div className="shrink-0 ml-2">{levelControls}</div>
          </div>

          {/* Workload card grid */}
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: `repeat(${cols}, 200px)`,
              gap: '6px',
              padding: '8px 14px 14px',
            }}
          >
            {workloadCards.map(card => {
              const statusClass = card.status === 'unhealthy' ? 'grid-card-unhealthy'
                : card.status === 'degraded' ? 'grid-card-degraded'
                : card.status === 'unknown' ? 'grid-card-unknown' : ''
              return (
                <div
                  key={card.id}
                  className={`grid-card ${statusClass}`}
                  onClick={(e) => { e.stopPropagation(); onCardClick?.(card.id) }}
                >
                  <div className="grid-card-header">
                    {(() => {
                      const KindIcon = getTopologyIcon(card.kind)
                      return <KindIcon className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
                    })()}
                    <span className="grid-card-kind">{card.kind}</span>
                    <span className="grid-card-count">{card.resourceCount}</span>
                  </div>
                  <div className="grid-card-name">{card.name}</div>
                  {card.subtitle && (
                    <div className="grid-card-subtitle">{card.subtitle}</div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      </>
    )
  }

  // ── Level 3: Topology (full expanded container) ──
  return (
    <>
      {handles}
      <div
        className={`absolute left-0 box-border isolate overflow-hidden group-container-adjusted ${hideHeader ? '' : 'rounded-xl bg-theme-surface/40'}`}
        style={{
          width: width || '100%',
          height: height || '100%',
          ...(hideHeader ? {} : getBorderStyle())
        }}
      >
        {!hideHeader && (
          <div
            className="w-full relative flex items-center"
            style={{ ...getHeaderBgStyle(), padding: '12px 16px' }}
          >
            <div
              className="flex items-center gap-3 min-w-0 flex-1 group-header-scaled"
            >
              <Icon className="shrink-0 w-7 h-7" style={getIconStyle()} />
              <span className="font-bold truncate text-2xl" style={getLabelStyle()}>{name}</span>
              {label && (
                <span className="text-sm text-theme-text-secondary truncate">({label})</span>
              )}
              <span className="shrink-0 text-xs text-theme-text-secondary bg-theme-surface/50 rounded px-1.5 py-0.5">
                {nodeCount}
              </span>
            </div>
            <div className="shrink-0 ml-2">{levelControls}</div>
          </div>
        )}
      </div>
    </>
  )
})
