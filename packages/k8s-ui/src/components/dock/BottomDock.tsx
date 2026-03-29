import { useState, useCallback, useRef, useEffect, ReactNode } from 'react'
import { DURATION_DOCK } from '../../utils/animation'
import { X, ChevronDown, ChevronUp, Terminal, FileText, Trash2, Layers, Maximize2, Minimize2 } from 'lucide-react'
import { clsx } from 'clsx'
import { useDock, DockTab } from './DockContext'
import { useRegisterShortcut } from '../../hooks/useKeyboardShortcuts'

const MIN_HEIGHT = 200
const DEFAULT_HEIGHT = 400
const MAX_HEIGHT_RATIO = 0.7
const MAXIMIZED_TOP_OFFSET = 48

interface BottomDockProps {
  renderTabContent: (tab: DockTab, isActive: boolean) => ReactNode
  /** Offset from the left edge in px — use to avoid overlapping a fixed sidebar */
  leftOffset?: number
}

export function BottomDock({ renderTabContent, leftOffset = 0 }: BottomDockProps) {
  const { tabs, activeTabId, isExpanded, removeTab, setActiveTab, toggleExpanded, closeAll } = useDock()
  const [height, setHeight] = useState(DEFAULT_HEIGHT)
  const [isMaximized, setIsMaximized] = useState(false)
  const isDragging = useRef(false)
  const startY = useRef(0)
  const startHeight = useRef(0)

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

  const toggleMaximized = useCallback(() => {
    setIsMaximized(prev => !prev)
  }, [])

  useEffect(() => {
    if (!isMaximized) return
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setIsMaximized(false)
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [isMaximized])

  useRegisterShortcut({
    id: 'dock-toggle',
    keys: 'Ctrl+`',
    description: 'Toggle dock',
    category: 'Dock',
    scope: 'global',
    handler: () => toggleExpanded(),
    enabled: tabs.length > 0,
  })

  if (tabs.length === 0) {
    return null
  }

  const effectiveHeight = !isExpanded ? 36 : isMaximized ? `calc(100vh - ${MAXIMIZED_TOP_OFFSET}px)` : height

  return (
    <div
      className="fixed bottom-0 right-0 bg-theme-base border-t border-theme-border flex flex-col z-40 overflow-hidden"
      style={{ height: effectiveHeight, left: leftOffset, transition: `height ${DURATION_DOCK}ms cubic-bezier(0.4, 0, 0.2, 1), left ${DURATION_DOCK}ms ease-out` }}
    >
      {isExpanded && !isMaximized && (
        <div
          className="absolute top-0 left-0 right-0 h-1 cursor-ns-resize hover:bg-skyhook-500/50 transition-colors"
          onMouseDown={handleMouseDown}
        />
      )}

      <div className="flex items-center h-9 px-2 bg-theme-surface border-b border-theme-border">
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
              className="p-1 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              title="Close all"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          )}
          {isExpanded && (
            <button
              onClick={toggleMaximized}
              className="p-1 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              title={isMaximized ? 'Restore (Esc)' : 'Maximize'}
            >
              {isMaximized ? (
                <Minimize2 className="w-3.5 h-3.5" />
              ) : (
                <Maximize2 className="w-3.5 h-3.5" />
              )}
            </button>
          )}
          <button
            onClick={() => { if (isMaximized) setIsMaximized(false); toggleExpanded() }}
            className="p-1 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
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

      <div className="flex-1 overflow-hidden w-full relative">
        {tabs.map(tab => (
          <div
            key={tab.id}
            className={tab.id === activeTabId ? 'absolute inset-0' : 'absolute inset-0 invisible'}
          >
            {renderTabContent(tab, tab.id === activeTabId)}
          </div>
        ))}
      </div>
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
  const Icon = tab.type === 'terminal' || tab.type === 'node-terminal' || tab.type === 'local-terminal'
    ? Terminal : tab.type === 'workload-logs' ? Layers : FileText

  return (
    <div
      className={clsx(
        'flex items-center gap-1.5 px-2 py-1 rounded text-xs cursor-pointer group',
        isActive
          ? 'bg-theme-elevated text-theme-text-primary'
          : 'text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated/50'
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
        className="p-0.5 rounded opacity-0 group-hover:opacity-100 hover:bg-theme-hover"
      >
        <X className="w-3 h-3" />
      </button>
    </div>
  )
}
