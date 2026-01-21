import { memo } from 'react'
import { NodeProps, Handle, Position } from '@xyflow/react'
import { ChevronDown, ChevronRight, Box, Tag } from 'lucide-react'
import { clsx } from 'clsx'

interface GroupNodeData {
  type: 'namespace' | 'app' | 'label'
  name: string
  label?: string
  nodeCount: number
  collapsed: boolean
  onToggleCollapse: (groupId: string) => void
}

export const GroupNode = memo(function GroupNode({
  id,
  data,
  width,
  height,
}: NodeProps & { data: GroupNodeData }) {
  const { type, name, label, nodeCount, collapsed, onToggleCollapse } = data

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

  const getBorderColor = () => {
    switch (type) {
      case 'namespace':
        return 'border-blue-500/40'
      case 'app':
        return 'border-emerald-500/40'
      case 'label':
        return 'border-amber-500/40'
      default:
        return 'border-theme-border'
    }
  }

  const getHeaderBgColor = () => {
    switch (type) {
      case 'namespace':
        return 'bg-blue-500/20'
      case 'app':
        return 'bg-emerald-500/20'
      case 'label':
        return 'bg-amber-500/20'
      default:
        return 'bg-theme-hover/50'
    }
  }

  const getLabelColor = () => {
    switch (type) {
      case 'namespace':
        return 'text-blue-300'
      case 'app':
        return 'text-emerald-300'
      case 'label':
        return 'text-amber-300'
      default:
        return 'text-theme-text-secondary'
    }
  }

  const getIconColor = () => {
    switch (type) {
      case 'namespace':
        return 'text-blue-400'
      case 'app':
        return 'text-emerald-400'
      case 'label':
        return 'text-amber-400'
      default:
        return 'text-theme-text-secondary'
    }
  }

  const Icon = getIcon()

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
          className={clsx(
            'rounded-xl border-2 p-4 cursor-pointer hover:border-opacity-70 transition-all',
            getBorderColor(),
            getHeaderBgColor()
          )}
          onClick={() => onToggleCollapse(id)}
        >
          <div className="flex items-center gap-4">
            <ChevronRight className={clsx('w-8 h-8', getIconColor())} />
            <Icon className={clsx('w-9 h-9', getIconColor())} />
            <span className={clsx('text-4xl font-bold', getLabelColor())}>{name}</span>
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
      <div
        className={clsx(
          'absolute top-0 left-0 rounded-xl border-2 box-border isolate overflow-hidden',
          getBorderColor(),
          'bg-theme-surface/40'
        )}
        style={{ width: width || '100%', height: height || '100%' }}
      >
        {/* Header bar - no margin, let overflow-hidden clip to border radius */}
        <div
          className={clsx(
            'flex items-center gap-4 px-6 py-5 cursor-pointer',
            getHeaderBgColor()
          )}
          onClick={() => onToggleCollapse(id)}
        >
          <ChevronDown className={clsx('w-8 h-8 flex-shrink-0', getIconColor())} />
          <Icon className={clsx('w-9 h-9 flex-shrink-0', getIconColor())} />
          <span className={clsx('text-4xl font-bold truncate', getLabelColor())}>{name}</span>
          {label && (
            <span className="text-sm text-theme-text-secondary truncate">({label})</span>
          )}
          <span className="ml-auto flex-shrink-0 text-xl font-semibold text-theme-text-secondary bg-theme-surface/60 px-4 py-2 rounded-xl">
            {nodeCount}
          </span>
        </div>
      </div>

      <Handle
        type="source"
        position={Position.Right}
        className="!bg-transparent !border-0 !w-0 !h-0"
      />
    </>
  )
})
