import { useState, useCallback, useRef, useEffect } from 'react'
import { X, ChevronDown, ChevronUp, Terminal, FileText, Trash2 } from 'lucide-react'
import { clsx } from 'clsx'
import { useDock, DockTab } from './DockContext'
import { TerminalTab } from './TerminalTab'
import { LogsTab } from './LogsTab'

const MIN_HEIGHT = 200
const DEFAULT_HEIGHT = 300
const MAX_HEIGHT_RATIO = 0.7

export function BottomDock() {
  const { tabs, activeTabId, isExpanded, removeTab, setActiveTab, toggleExpanded, closeAll } = useDock()
  const [height, setHeight] = useState(DEFAULT_HEIGHT)
  const isDragging = useRef(false)
  const startY = useRef(0)
  const startHeight = useRef(0)

  // Handle resize drag - must be before any early returns (Rules of Hooks)
  const handleMouseDown = useCallback((e: React.MouseEvent) => {
    isDragging.current = true
    startY.current = e.clientY
    startHeight.current = height
    document.body.style.cursor = 'ns-resize'
    document.body.style.userSelect = 'none'
  }, [height])

  useEffect(() => {
    const handleMouseMove = (e: MouseEvent) => {
      if (!isDragging.current) return
      const maxHeight = window.innerHeight * MAX_HEIGHT_RATIO
      const newHeight = Math.min(maxHeight, Math.max(MIN_HEIGHT, startHeight.current - (e.clientY - startY.current)))
      setHeight(newHeight)
    }

    const handleMouseUp = () => {
      isDragging.current = false
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }

    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)

    return () => {
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
    }
  }, [])

  // Don't render if no tabs - AFTER all hooks
  if (tabs.length === 0) {
    return null
  }

  const activeTab = tabs.find(t => t.id === activeTabId)

  return (
    <div
      className="fixed bottom-0 left-0 right-0 bg-slate-900 border-t border-slate-700 flex flex-col z-40"
      style={{ height: isExpanded ? height : 36 }}
    >
      {/* Resize handle */}
      {isExpanded && (
        <div
          className="absolute top-0 left-0 right-0 h-1 cursor-ns-resize hover:bg-blue-500/50 transition-colors"
          onMouseDown={handleMouseDown}
        />
      )}

      {/* Tab bar */}
      <div className="flex items-center h-9 px-2 bg-slate-800 border-b border-slate-700">
        <div className="flex items-center gap-1 flex-1 overflow-x-auto">
          {tabs.map(tab => (
            <TabButton
              key={tab.id}
              tab={tab}
              isActive={tab.id === activeTabId}
              onSelect={() => {
                setActiveTab(tab.id)
                if (!isExpanded) toggleExpanded()
              }}
              onClose={() => removeTab(tab.id)}
            />
          ))}
        </div>

        <div className="flex items-center gap-1 ml-2">
          {tabs.length > 1 && (
            <button
              onClick={closeAll}
              className="p-1 text-slate-400 hover:text-white hover:bg-slate-700 rounded"
              title="Close all"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          )}
          <button
            onClick={toggleExpanded}
            className="p-1 text-slate-400 hover:text-white hover:bg-slate-700 rounded"
            title={isExpanded ? 'Collapse' : 'Expand'}
          >
            {isExpanded ? (
              <ChevronDown className="w-4 h-4" />
            ) : (
              <ChevronUp className="w-4 h-4" />
            )}
          </button>
        </div>
      </div>

      {/* Tab content - render all tabs but only show active one to preserve state */}
      {isExpanded && (
        <div className="flex-1 overflow-hidden w-full relative">
          {tabs.map(tab => (
            <div
              key={tab.id}
              className={tab.id === activeTabId ? 'absolute inset-0' : 'absolute inset-0 invisible'}
            >
              <TabContent tab={tab} isActive={tab.id === activeTabId} />
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function TabButton({
  tab,
  isActive,
  onSelect,
  onClose,
}: {
  tab: DockTab
  isActive: boolean
  onSelect: () => void
  onClose: () => void
}) {
  const Icon = tab.type === 'terminal' ? Terminal : FileText

  return (
    <div
      className={clsx(
        'flex items-center gap-1.5 px-2 py-1 rounded text-xs cursor-pointer group',
        isActive
          ? 'bg-slate-700 text-white'
          : 'text-slate-400 hover:text-white hover:bg-slate-700/50'
      )}
      onClick={onSelect}
    >
      <Icon className="w-3.5 h-3.5" />
      <span className="truncate max-w-[120px]">{tab.title}</span>
      <button
        onClick={(e) => {
          e.stopPropagation()
          onClose()
        }}
        className="p-0.5 rounded opacity-0 group-hover:opacity-100 hover:bg-slate-600"
      >
        <X className="w-3 h-3" />
      </button>
    </div>
  )
}

function TabContent({ tab, isActive }: { tab: DockTab; isActive: boolean }) {
  if (tab.type === 'terminal') {
    return (
      <TerminalTab
        namespace={tab.namespace!}
        podName={tab.podName!}
        containerName={tab.containerName!}
        containers={tab.containers!}
        isActive={isActive}
      />
    )
  }

  if (tab.type === 'logs') {
    return (
      <LogsTab
        namespace={tab.namespace!}
        podName={tab.podName!}
        containers={tab.containers!}
        initialContainer={tab.containerName}
      />
    )
  }

  return null
}
