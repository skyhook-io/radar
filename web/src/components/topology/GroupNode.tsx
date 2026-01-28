import { memo } from 'react'
import { NodeProps, Handle, Position, useViewport } from '@xyflow/react'
import { ChevronDown, ChevronRight, Box, Tag } from 'lucide-react'

interface GroupNodeData {
  type: 'namespace' | 'app' | 'label'
  name: string
  label?: string
  nodeCount: number
  collapsed: boolean
  onToggleCollapse: (groupId: string) => void
  hideHeader?: boolean
}

export const GroupNode = memo(function GroupNode({
  id,
  data,
  width,
  height,
}: NodeProps & { data: GroupNodeData }) {
  const { type, name, label, nodeCount, collapsed, onToggleCollapse, hideHeader } = data

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
    // Must set full 'border' property to override ReactFlow's --xy-node-border
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

  // Get viewport zoom to scale header adaptively
  // At low zoom (zoomed out), keep headers large for readability
  // At high zoom (zoomed in), reduce header size so it's not comically large
  // Start scaling down at zoom 0.5 for earlier reduction when zooming in
  const { zoom } = useViewport()
  const headerScale = Math.max(0.35, Math.min(1, 0.5 / zoom))

  // When collapsed, render as a compact card
  if (collapsed) {
    return (
      <>
        <Handle
          type="target"
          position={Position.Left}
          className="!bg-transparent !border-0 !w-0 !h-0"
        />

        <div
          className="rounded-xl p-4 cursor-pointer transition-all"
          onClick={() => onToggleCollapse(id)}
          style={{ transform: `scale(${headerScale})`, transformOrigin: 'top left', ...getBorderStyle(), ...getHeaderBgStyle() }}
        >
          <div className="flex items-center gap-4">
            <ChevronRight className="w-8 h-8" style={getIconStyle()} />
            <Icon className="w-9 h-9" style={getIconStyle()} />
            <span className="text-4xl font-bold" style={getLabelStyle()}>{name}</span>
            {label && (
              <span className="text-sm text-theme-text-secondary">({label})</span>
            )}
          </div>
          <div className="mt-3 text-xl text-theme-text-secondary">
            {nodeCount} {nodeCount === 1 ? 'resource' : 'resources'}
          </div>
        </div>

        <Handle
          type="source"
          position={Position.Right}
          className="!bg-transparent !border-0 !w-0 !h-0"
        />
      </>
    )
  }

  // When expanded, render as a container with header
  // Children are rendered automatically by ReactFlow via parentId
  return (
    <>
      <Handle
        type="target"
        position={Position.Left}
        className="!bg-transparent !border-0 !w-0 !h-0"
      />

      {/* Container with border - use explicit dimensions from props */}
      {/* Top position adjusts based on zoom to keep border tight around content */}
      <div
        className="absolute left-0 rounded-xl box-border isolate overflow-hidden bg-theme-surface/40"
        style={{
          width: width || '100%',
          height: `calc(${height || '100%'}px - ${60 * (1 - headerScale)}px)`,
          top: `${60 * (1 - headerScale)}px`,
          ...getBorderStyle()
        }}
      >
        {/* Header bar - content scales based on zoom level for readability */}
        {/* Hidden when hideHeader is true (single namespace view) */}
        {!hideHeader && (
          <div
            className="flex items-center cursor-pointer"
            onClick={() => onToggleCollapse(id)}
            style={{
              padding: `${20 * headerScale}px ${24 * headerScale}px`,
              gap: `${16 * headerScale}px`,
              ...getHeaderBgStyle()
            }}
          >
            <ChevronDown
              className="shrink-0"
              style={{ width: 32 * headerScale, height: 32 * headerScale, ...getIconStyle() }}
            />
            <Icon
              className="shrink-0"
              style={{ width: 36 * headerScale, height: 36 * headerScale, ...getIconStyle() }}
            />
            <span
              className="font-bold truncate"
              style={{ fontSize: 36 * headerScale, ...getLabelStyle() }}
            >
              {name}
            </span>
            {label && (
              <span
                className="text-theme-text-secondary truncate"
                style={{ fontSize: 14 * headerScale }}
              >
                ({label})
              </span>
            )}
            <span
              className="ml-auto shrink-0 font-semibold text-theme-text-secondary bg-theme-surface/60 rounded-xl"
              style={{
                fontSize: 20 * headerScale,
                padding: `${8 * headerScale}px ${16 * headerScale}px`
              }}
            >
              {nodeCount}
            </span>
          </div>
        )}
      </div>

      <Handle
        type="source"
        position={Position.Right}
        className="!bg-transparent !border-0 !w-0 !h-0"
      />
    </>
  )
})
