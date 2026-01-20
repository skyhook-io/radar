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

  const getColor = () => {
    switch (type) {
      case 'namespace':
        return 'border-indigo-500/50 bg-indigo-500/5'
      case 'app':
        return 'border-emerald-500/50 bg-emerald-500/5'
      case 'label':
        return 'border-amber-500/50 bg-amber-500/5'
      default:
        return 'border-slate-500/50 bg-slate-500/5'
    }
  }

  const Icon = getIcon()

  return (
    <>
      <Handle
        type="target"
        position={Position.Left}
        className="!bg-transparent !border-0 !w-0 !h-0"
      />

      <div
        className={clsx(
          'rounded-lg border-2 border-dashed transition-all',
          getColor(),
          collapsed ? 'p-3' : 'p-2'
        )}
        style={collapsed ? {} : { minWidth: 200, minHeight: 100 }}
      >
        {/* Header */}
        <div
          className="flex items-center gap-2 cursor-pointer select-none"
          onClick={() => onToggleCollapse(id)}
        >
          {collapsed ? (
            <ChevronRight className="w-4 h-4 text-slate-400" />
          ) : (
            <ChevronDown className="w-4 h-4 text-slate-400" />
          )}
          <Icon className="w-4 h-4 text-slate-400" />
          <span className="text-sm font-medium text-slate-300">{name}</span>
          {label && (
            <span className="text-xs text-slate-500">({label})</span>
          )}
          <span className="ml-auto text-xs text-slate-500 bg-slate-700/50 px-2 py-0.5 rounded">
            {nodeCount} {nodeCount === 1 ? 'resource' : 'resources'}
          </span>
        </div>

        {/* Collapsed summary */}
        {collapsed && (
          <div className="mt-2 text-xs text-slate-500">
            Click to expand
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
